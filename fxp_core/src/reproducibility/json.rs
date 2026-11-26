// SPDX-License-Identifier: MIT
//! Small dependency-free JSON codec for bundle metadata. Numbers retain their exact text.
use std::io;
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Json {
    Null,
    Bool(bool),
    Number(String),
    String(String),
    Array(Vec<Json>),
    Object(Map),
}
/// Object fields as one key-sorted vector. Bundles parse to millions of small
/// objects; a B-tree node costs ~600 bytes even for two fields.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct Map(Vec<(String, Json)>);
impl Map {
    pub(crate) fn new() -> Self {
        Self::default()
    }
    pub(crate) fn with_capacity(n: usize) -> Self {
        Self(Vec::with_capacity(n))
    }
    fn find(&self, key: &str) -> Result<usize, usize> {
        self.0.binary_search_by(|(k, _)| k.as_str().cmp(key))
    }
    pub(crate) fn get(&self, key: &str) -> Option<&Json> {
        self.find(key).ok().map(|i| &self.0[i].1)
    }
    pub(crate) fn get_mut(&mut self, key: &str) -> Option<&mut Json> {
        self.find(key).ok().map(|i| &mut self.0[i].1)
    }
    pub(crate) fn contains_key(&self, key: &str) -> bool {
        self.find(key).is_ok()
    }
    pub(crate) fn insert(&mut self, key: String, value: Json) -> Option<Json> {
        if self.0.last().is_none_or(|(k, _)| *k < key) {
            self.0.push((key, value));
            return None;
        }
        match self.find(&key) {
            Ok(i) => Some(std::mem::replace(&mut self.0[i].1, value)),
            Err(i) => {
                self.0.insert(i, (key, value));
                None
            }
        }
    }
    pub(crate) fn remove(&mut self, key: &str) -> Option<Json> {
        self.find(key).ok().map(|i| self.0.remove(i).1)
    }
    pub(crate) fn retain(&mut self, mut keep: impl FnMut(&String, &mut Json) -> bool) {
        self.0.retain_mut(|(k, v)| keep(k, v));
    }
    pub(crate) fn len(&self) -> usize {
        self.0.len()
    }
    pub(crate) fn iter(&self) -> impl DoubleEndedIterator<Item = (&String, &Json)> {
        self.0.iter().map(|(k, v)| (k, v))
    }
    pub(crate) fn values(&self) -> impl DoubleEndedIterator<Item = &Json> {
        self.0.iter().map(|(_, v)| v)
    }
    pub(crate) fn values_mut(&mut self) -> impl DoubleEndedIterator<Item = &mut Json> {
        self.0.iter_mut().map(|(_, v)| v)
    }
}
impl FromIterator<(String, Json)> for Map {
    /// Later duplicates replace earlier ones, as with `BTreeMap`.
    fn from_iter<I: IntoIterator<Item = (String, Json)>>(fields: I) -> Self {
        let mut v: Vec<_> = fields.into_iter().collect();
        if !v.is_sorted_by(|a, b| a.0 < b.0) {
            v.reverse();
            v.sort_by(|a, b| a.0.cmp(&b.0));
            v.dedup_by(|later, earlier| later.0 == earlier.0);
        }
        v.shrink_to_fit();
        Self(v)
    }
}
impl IntoIterator for Map {
    type Item = (String, Json);
    type IntoIter = std::vec::IntoIter<(String, Json)>;
    fn into_iter(self) -> Self::IntoIter {
        self.0.into_iter()
    }
}
impl<'a> IntoIterator for &'a Map {
    type Item = (&'a String, &'a Json);
    type IntoIter = std::iter::Map<
        std::slice::Iter<'a, (String, Json)>,
        fn(&'a (String, Json)) -> (&'a String, &'a Json),
    >;
    fn into_iter(self) -> Self::IntoIter {
        self.0.iter().map(|(k, v)| (k, v))
    }
}
impl Json {
    pub(crate) fn string(s: impl Into<String>) -> Self {
        Self::String(s.into())
    }
    pub(crate) fn number(n: u64) -> Self {
        Self::Number(n.to_string())
    }
    pub(crate) fn object(fields: impl IntoIterator<Item = (impl Into<String>, Json)>) -> Self {
        Self::Object(fields.into_iter().map(|(k, v)| (k.into(), v)).collect())
    }
    pub(crate) fn get(&self, key: &str) -> io::Result<&Json> {
        match self {
            Self::Object(m) => m
                .get(key)
                .ok_or_else(|| super::invalid(format!("missing {key}"))),
            _ => Err(super::invalid("expected JSON object")),
        }
    }
    pub(crate) fn text(&self) -> io::Result<&str> {
        match self {
            Self::String(s) => Ok(s),
            _ => Err(super::invalid("expected JSON string")),
        }
    }
    pub(crate) fn encode(&self) -> String {
        let mut out = String::new();
        self.encode_into(&mut out);
        out
    }
    /// Byte length of `encode()` without building the string.
    pub(crate) fn encoded_len(&self) -> usize {
        fn text(s: &str) -> usize {
            2 + s
                .chars()
                .map(|c| match c {
                    '"' | '\\' => 2,
                    c if c < ' ' => 6,
                    c => c.len_utf8(),
                })
                .sum::<usize>()
        }
        match self {
            Self::Null => 4,
            Self::Bool(true) => 4,
            Self::Bool(false) => 5,
            Self::Number(n) => n.len(),
            Self::String(s) => text(s),
            Self::Array(a) => {
                2 + a.len().saturating_sub(1) + a.iter().map(Self::encoded_len).sum::<usize>()
            }
            Self::Object(m) => {
                2 + m.len().saturating_sub(1)
                    + m.iter()
                        .map(|(k, v)| text(k) + 1 + v.encoded_len())
                        .sum::<usize>()
            }
        }
    }
    fn encode_string(s: &str, out: &mut String) {
        out.push('"');
        for c in s.chars() {
            match c {
                '"' => out.push_str("\\\""),
                '\\' => out.push_str("\\\\"),
                c if c < ' ' => {
                    use std::fmt::Write;
                    write!(out, "\\u{:04x}", c as u32).unwrap();
                }
                c => out.push(c),
            }
        }
        out.push('"');
    }
    // Append recursively to one buffer; large timelines must not allocate
    // encoded copies for every row, then join and copy the whole array again.
    fn encode_into(&self, out: &mut String) {
        match self {
            Self::Null => out.push_str("null"),
            Self::Bool(true) => out.push_str("true"),
            Self::Bool(false) => out.push_str("false"),
            Self::Number(n) => out.push_str(n),
            Self::String(s) => Self::encode_string(s, out),
            Self::Array(a) => {
                out.push('[');
                for (i, value) in a.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    value.encode_into(out);
                }
                out.push(']');
            }
            Self::Object(m) => {
                out.push('{');
                for (i, (key, value)) in m.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    Self::encode_string(key, out);
                    out.push(':');
                    value.encode_into(out);
                }
                out.push('}');
            }
        }
    }
    pub(crate) fn parse(s: &str) -> io::Result<Self> {
        let mut p = Parser {
            s: s.as_bytes(),
            i: 0,
        };
        let value = p.value(0)?;
        p.ws();
        if p.i != p.s.len() {
            return Err(super::invalid("trailing JSON data"));
        }
        Ok(value)
    }
}
struct Parser<'a> {
    s: &'a [u8],
    i: usize,
}
impl Parser<'_> {
    fn ws(&mut self) {
        while self.s.get(self.i).is_some_and(|b| b" \t\r\n".contains(b)) {
            self.i += 1;
        }
    }
    fn take(&mut self, b: u8) -> bool {
        self.ws();
        if self.s.get(self.i) == Some(&b) {
            self.i += 1;
            true
        } else {
            false
        }
    }
    fn expect(&mut self, b: u8) -> io::Result<()> {
        if self.take(b) {
            Ok(())
        } else {
            Err(super::invalid("invalid JSON punctuation"))
        }
    }
    fn hex4(&mut self) -> io::Result<u16> {
        let bytes = self
            .s
            .get(self.i..self.i + 4)
            .ok_or_else(|| super::invalid("short JSON escape"))?;
        let text = std::str::from_utf8(bytes).map_err(super::invalid)?;
        let n = u16::from_str_radix(text, 16).map_err(super::invalid)?;
        self.i += 4;
        Ok(n)
    }
    fn string(&mut self) -> io::Result<String> {
        self.expect(b'"')?;
        let mut out = Vec::new();
        loop {
            let b = *self
                .s
                .get(self.i)
                .ok_or_else(|| super::invalid("unterminated JSON string"))?;
            self.i += 1;
            match b {
                b'"' => return String::from_utf8(out).map_err(super::invalid),
                b'\\' => {
                    let e = *self
                        .s
                        .get(self.i)
                        .ok_or_else(|| super::invalid("short JSON escape"))?;
                    self.i += 1;
                    match e {
                        b'"' | b'\\' | b'/' => out.push(e),
                        b'b' => out.push(8),
                        b'f' => out.push(12),
                        b'n' => out.push(10),
                        b'r' => out.push(13),
                        b't' => out.push(9),
                        b'u' => {
                            let a = self.hex4()?;
                            let cp = if (0xd800..=0xdbff).contains(&a) {
                                if self.s.get(self.i..self.i + 2) != Some(b"\\u") {
                                    return Err(super::invalid("unpaired JSON surrogate"));
                                }
                                self.i += 2;
                                let b = self.hex4()?;
                                if !(0xdc00..=0xdfff).contains(&b) {
                                    return Err(super::invalid("unpaired JSON surrogate"));
                                }
                                0x10000 + ((u32::from(a) - 0xd800) << 10) + u32::from(b) - 0xdc00
                            } else {
                                u32::from(a)
                            };
                            let c = char::from_u32(cp)
                                .ok_or_else(|| super::invalid("invalid JSON codepoint"))?;
                            let mut buf = [0; 4];
                            out.extend_from_slice(c.encode_utf8(&mut buf).as_bytes());
                        }
                        _ => return Err(super::invalid("invalid JSON escape")),
                    }
                }
                0..=31 => return Err(super::invalid("control character in JSON string")),
                _ => out.push(b),
            }
        }
    }
    fn value(&mut self, depth: usize) -> io::Result<Json> {
        if depth > 64 {
            return Err(super::invalid("JSON nesting exceeds limit"));
        }
        self.ws();
        match self.s.get(self.i).copied() {
            Some(b'"') => Ok(Json::String(self.string()?)),
            Some(b'{') => {
                self.i += 1;
                let mut fields = Vec::new();
                if self.take(b'}') {
                    return Ok(Json::Object(Map::new()));
                }
                loop {
                    let k = self.string()?;
                    self.expect(b':')?;
                    fields.push((k, self.value(depth + 1)?));
                    if self.take(b'}') {
                        break;
                    }
                    self.expect(b',')?;
                }
                // Sort once: inserting unsorted keys one by one is quadratic.
                fields.sort_by(|a: &(String, Json), b| a.0.cmp(&b.0));
                if fields.windows(2).any(|w| w[0].0 == w[1].0) {
                    return Err(super::invalid("duplicate JSON key"));
                }
                fields.shrink_to_fit();
                Ok(Json::Object(Map(fields)))
            }
            Some(b'[') => {
                self.i += 1;
                let mut a = Vec::new();
                if self.take(b']') {
                    return Ok(Json::Array(a));
                }
                loop {
                    a.push(self.value(depth + 1)?);
                    if self.take(b']') {
                        break;
                    }
                    self.expect(b',')?;
                }
                Ok(Json::Array(a))
            }
            Some(b'n') if self.s.get(self.i..self.i + 4) == Some(b"null") => {
                self.i += 4;
                Ok(Json::Null)
            }
            Some(b't') if self.s.get(self.i..self.i + 4) == Some(b"true") => {
                self.i += 4;
                Ok(Json::Bool(true))
            }
            Some(b'f') if self.s.get(self.i..self.i + 5) == Some(b"false") => {
                self.i += 5;
                Ok(Json::Bool(false))
            }
            Some(b'-' | b'0'..=b'9') => {
                let start = self.i;
                if self.s[self.i] == b'-' {
                    self.i += 1;
                }
                match self.s.get(self.i) {
                    Some(b'0') => self.i += 1,
                    Some(b'1'..=b'9') => self.digits(),
                    _ => return Err(super::invalid("invalid JSON number")),
                }
                if self.s.get(self.i) == Some(&b'.') {
                    self.i += 1;
                    self.required_digits()?;
                }
                if self.s.get(self.i).is_some_and(|b| b"eE".contains(b)) {
                    self.i += 1;
                    if self.s.get(self.i).is_some_and(|b| b"+-".contains(b)) {
                        self.i += 1;
                    }
                    self.required_digits()?;
                }
                Ok(Json::Number(
                    std::str::from_utf8(&self.s[start..self.i])
                        .map_err(super::invalid)?
                        .into(),
                ))
            }
            _ => Err(super::invalid("invalid JSON value")),
        }
    }
    fn digits(&mut self) {
        while self.s.get(self.i).is_some_and(u8::is_ascii_digit) {
            self.i += 1;
        }
    }
    fn required_digits(&mut self) -> io::Result<()> {
        let start = self.i;
        self.digits();
        if self.i == start {
            Err(super::invalid("invalid JSON number"))
        } else {
            Ok(())
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn object_fields_stay_sorted_and_unique() {
        let mut m: Map = [("b", 1), ("a", 2), ("b", 3)]
            .into_iter()
            .map(|(k, v)| (k.to_string(), Json::number(v)))
            .collect();
        assert_eq!(Json::Object(m.clone()).encode(), r#"{"a":2,"b":3}"#);
        assert_eq!(m.insert("c".into(), Json::Null), None);
        assert_eq!(m.insert("0".into(), Json::Null), None);
        assert_eq!(
            m.insert("a".into(), Json::Bool(true)),
            Some(Json::number(2))
        );
        assert_eq!(m.remove("c"), Some(Json::Null));
        assert!(m.contains_key("0") && !m.contains_key("c"));
        assert_eq!(Json::Object(m).encode(), r#"{"0":null,"a":true,"b":3}"#);
        let unsorted = Json::parse(r#"{"z":1,"a":{"y":[],"b":"\u0001\"\\é"}}"#).unwrap();
        assert_eq!(
            unsorted.encode(),
            r#"{"a":{"b":"\u0001\"\\é","y":[]},"z":1}"#
        );
        assert_eq!(unsorted.encoded_len(), unsorted.encode().len());
        assert!(Json::parse(r#"{"a":1,"a":2}"#).is_err());
    }
    #[test]
    fn metadata_json() {
        for s in [
            r#"{"seed":123456789012345678901234,"emoji":"\ud83d\ude00","a":[true,false,null,-1.3e+2]}"#,
            r#"{"é":"\n\t\\\""}"#,
        ] {
            let j = Json::parse(s).unwrap();
            assert_eq!(Json::parse(&j.encode()).unwrap(), j);
        }
        for s in [
            "01",
            "1.",
            "[1,]",
            "{\"x\":1,\"x\":2}",
            "\"\\ud800\"",
            "true false",
        ] {
            assert!(Json::parse(s).is_err(), "{s}");
        }
    }
}

#[cfg(test)]
mod independent_tests {
    use super::*;
    use std::io::Write;
    use std::process::{Command, Stdio};
    #[test]
    fn json_metadata_matches_python_encoder_and_decoder() {
        let script =
            std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../scripts/repro_vectors.py");
        let generated = Command::new("python3")
            .arg(&script)
            .arg("--metadata")
            .output()
            .unwrap();
        assert!(generated.status.success());
        let parsed = Json::parse(std::str::from_utf8(&generated.stdout).unwrap()).unwrap();
        assert_eq!(parsed.get("seed").unwrap(), &Json::number(7));
        assert_eq!(parsed.get("unicode").unwrap().text().unwrap(), "é😀");
        assert_eq!(
            parsed.get("types").unwrap(),
            &Json::Array(vec![Json::Bool(true), Json::Bool(false), Json::Null])
        );
        assert_eq!(
            parsed.get("numbers").unwrap(),
            &Json::Array(
                ["0", "-12", "1.25", "1e+40"]
                    .into_iter()
                    .map(|n| Json::Number(n.into()))
                    .collect()
            )
        );
        let mut child = Command::new("python3")
            .arg(script)
            .arg("--check-metadata")
            .stdin(Stdio::piped())
            .spawn()
            .unwrap();
        child
            .stdin
            .take()
            .unwrap()
            .write_all(parsed.encode().as_bytes())
            .unwrap();
        assert!(child.wait().unwrap().success());
    }
}
