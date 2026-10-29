// SPDX-License-Identifier: MIT
//! Small dependency-free JSON codec for bundle metadata. Numbers retain their exact text.
use std::collections::BTreeMap;
use std::io;
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) enum Json {
    Null,
    Bool(bool),
    Number(String),
    String(String),
    Array(Vec<Json>),
    Object(BTreeMap<String, Json>),
}
impl Json {
    pub(super) fn string(s: impl Into<String>) -> Self {
        Self::String(s.into())
    }
    pub(super) fn number(n: u64) -> Self {
        Self::Number(n.to_string())
    }
    pub(super) fn object(fields: impl IntoIterator<Item = (impl Into<String>, Json)>) -> Self {
        Self::Object(fields.into_iter().map(|(k, v)| (k.into(), v)).collect())
    }
    pub(super) fn get(&self, key: &str) -> io::Result<&Json> {
        match self {
            Self::Object(m) => m
                .get(key)
                .ok_or_else(|| super::invalid(format!("missing {key}"))),
            _ => Err(super::invalid("expected JSON object")),
        }
    }
    pub(super) fn text(&self) -> io::Result<&str> {
        match self {
            Self::String(s) => Ok(s),
            _ => Err(super::invalid("expected JSON string")),
        }
    }
    pub(super) fn encode(&self) -> String {
        match self {
            Self::Null => "null".into(),
            Self::Bool(b) => b.to_string(),
            Self::Number(n) => n.clone(),
            Self::String(s) => {
                let mut out = String::from("\"");
                for c in s.chars() {
                    match c {
                        '"' => out.push_str("\\\""),
                        '\\' => out.push_str("\\\\"),
                        c if c < ' ' => out.push_str(&format!("\\u{:04x}", c as u32)),
                        c => out.push(c),
                    }
                }
                out.push('"');
                out
            }
            Self::Array(a) => format!(
                "[{}]",
                a.iter().map(Self::encode).collect::<Vec<_>>().join(",")
            ),
            Self::Object(m) => format!(
                "{{{}}}",
                m.iter()
                    .map(|(k, v)| format!("{}:{}", Self::string(k).encode(), v.encode()))
                    .collect::<Vec<_>>()
                    .join(",")
            ),
        }
    }
    pub(super) fn parse(s: &str) -> io::Result<Self> {
        if s.len() > 4 * 1024 * 1024 {
            return Err(super::invalid("JSON exceeds size limit"));
        }
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
                let mut m = BTreeMap::new();
                if self.take(b'}') {
                    return Ok(Json::Object(m));
                }
                loop {
                    let k = self.string()?;
                    self.expect(b':')?;
                    let v = self.value(depth + 1)?;
                    if m.insert(k, v).is_some() {
                        return Err(super::invalid("duplicate JSON key"));
                    }
                    if self.take(b'}') {
                        break;
                    }
                    self.expect(b',')?;
                }
                Ok(Json::Object(m))
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
