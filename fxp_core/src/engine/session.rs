// SPDX-License-Identifier: MIT

use std::str::FromStr;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SessionSchedule {
    pub close: UtcTimeOfDay,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct UtcTimeOfDay {
    seconds: u32,
}

impl SessionSchedule {
    pub(crate) fn next_close(self, after: i64) -> i64 {
        let close = after.div_euclid(86_400) * 86_400 + i64::from(self.close.seconds);
        if close <= after {
            close + 86_400
        } else {
            close
        }
    }
}

impl FromStr for UtcTimeOfDay {
    type Err = String;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        let parts: Vec<_> = value.split(':').collect();
        if !(parts.len() == 2 || parts.len() == 3)
            || parts
                .iter()
                .any(|p| p.len() != 2 || !p.bytes().all(|c| c.is_ascii_digit()))
        {
            return Err("expected UTC HH:MM[:SS]".into());
        }
        let hour: u32 = parts[0].parse().unwrap();
        let minute: u32 = parts[1].parse().unwrap();
        let second: u32 = parts.get(2).map_or(0, |s| s.parse().unwrap());
        if hour > 23 || minute > 59 || second > 59 {
            return Err("UTC time of day is out of range".into());
        }
        Ok(Self {
            seconds: hour * 3600 + minute * 60 + second,
        })
    }
}

impl FromStr for SessionSchedule {
    type Err = String;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        let mut close = None;
        for entry in value.split(',') {
            let (key, value) = entry.split_once('=').ok_or("expected key=value")?;
            match key {
                "close" if close.is_none() => close = Some(value.parse()?),
                "close" => return Err("duplicate close".into()),
                _ => return Err(format!("unknown schedule key {key:?}")),
            }
        }
        Ok(Self {
            close: close.ok_or("close is required")?,
        })
    }
}

pub(crate) fn expiry_instant(date: &str) -> Result<i64, String> {
    let bytes = date.as_bytes();
    if bytes.len() != 10
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes
            .iter()
            .enumerate()
            .any(|(i, c)| i != 4 && i != 7 && !c.is_ascii_digit())
    {
        return Err("expire_date must be YYYY-MM-DD".into());
    }
    let year: i64 = date[..4].parse().unwrap();
    let month: i64 = date[5..7].parse().unwrap();
    let day: i64 = date[8..].parse().unwrap();
    let leap = year % 4 == 0 && (year % 100 != 0 || year % 400 == 0);
    let days = match month {
        2 if leap => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        _ => return Err("invalid expire_date month".into()),
    };
    if year == 0 || day < 1 || day > days {
        return Err("invalid expire_date".into());
    }
    // Gregorian civil date to days since 1970-01-01 (March-based eras).
    let y = year - i64::from(month <= 2);
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let m = month + if month > 2 { -3 } else { 9 };
    let doy = (153 * m + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Ok((era * 146_097 + doe - 719_468 + 1) * 86_400)
}
