//! Reference program for rustcompat's TestMatchesRust: the Rust standard library answering the
//! same questions. Reads inputs from stdin, each a little-endian u32 length and that many bytes:
//! one letter naming the operation, then its argument text. Prints one line per input.
//! Floats are passed and printed as hex bit patterns so nothing is lost; text that may hold any
//! character is printed as hex (UTF-8 bytes, or code points).
use std::io::{Read, Write};

fn f64_of(s: &str) -> f64 {
    f64::from_bits(u64::from_str_radix(s, 16).unwrap_or(0))
}

fn bits(f: f64) -> String {
    if f.is_nan() { "NaN".into() } else { format!("{:016x}", f.to_bits()) }
}

fn hex(s: &str) -> String {
    s.bytes().map(|b| format!("{b:02x}")).collect()
}

fn answer(op: u8, arg: &str) -> String {
    let mut it = arg.split(':');
    let mut next = || it.next().unwrap_or("");
    match op {
        b'f' => arg.parse::<f64>().map(bits).unwrap_or_else(|_| "ERR".into()),
        b'g' => arg.parse::<f32>().map(|f| if f.is_nan() { "NaN".into() } else { format!("{:08x}", f.to_bits()) }).unwrap_or_else(|_| "ERR".into()),
        b'i' => {
            let (b, s) = (next(), &arg[arg.find(':').map_or(0, |i| i + 1)..]);
            let r = match b {
                "8" => s.parse::<i8>().map(|v| v as i64),
                "16" => s.parse::<i16>().map(|v| v as i64),
                "32" => s.parse::<i32>().map(|v| v as i64),
                _ => s.parse::<i64>(),
            };
            r.map(|v| v.to_string()).unwrap_or_else(|_| "ERR".into())
        }
        b'u' => {
            let (b, s) = (next(), &arg[arg.find(':').map_or(0, |i| i + 1)..]);
            let r = match b {
                "8" => s.parse::<u8>().map(|v| v as u64),
                "16" => s.parse::<u16>().map(|v| v as u64),
                "32" => s.parse::<u32>().map(|v| v as u64),
                _ => s.parse::<u64>(),
            };
            r.map(|v| v.to_string()).unwrap_or_else(|_| "ERR".into())
        }
        b'd' => format!("{}", f64_of(arg)),
        b'D' => format!("{}", f32::from_bits(u32::from_str_radix(arg, 16).unwrap_or(0))),
        b'x' => {
            let f = f64_of(next());
            let p: usize = next().parse().unwrap_or(0);
            format!("{:.*}", p, f)
        }
        b'e' => {
            let f = f64_of(next());
            let p: usize = next().parse().unwrap_or(0);
            format!("{:.*e}", p, f)
        }
        b'E' => format!("{:e}", f64_of(arg)),
        b'm' => {
            let (a, b, c) = (f64_of(next()), f64_of(next()), f64_of(next()));
            let (lo, hi) = if b <= c { (b, c) } else { (c, b) };
            let clamp = if lo.is_nan() || hi.is_nan() { "skip".to_string() } else { bits(a.clamp(lo, hi)) };
            format!("{} {} {}", bits(a.max(b)), bits(a.min(b)), clamp)
        }
        b'c' => {
            let f = f64_of(arg);
            format!("{} {} {} {}", f as i64, f as i32, f as u64, f as u32)
        }
        b'r' => {
            let a: i64 = next().parse().unwrap_or(0);
            let b: i64 = next().parse().unwrap_or(1);
            if b == 0 || (a == i64::MIN && b == -1) { "skip".into() } else { format!("{} {}", a.div_euclid(b), a.rem_euclid(b)) }
        }
        b'l' => {
            let c = u32::from_str_radix(arg, 16).ok().and_then(char::from_u32).unwrap_or('?');
            let low: Vec<String> = c.to_lowercase().map(|l| format!("{:x}", l as u32)).collect();
            format!("{} {}", low.join(","), c.is_whitespace())
        }
        b't' => format!("{} {} {}", hex(arg.trim()), hex(arg.trim_start()), hex(arg.trim_end())),
        _ => "BAD OP".into(),
    }
}

fn main() {
    let mut input = Vec::new();
    if std::io::stdin().read_to_end(&mut input).is_err() {
        return;
    }
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    let mut rest = &input[..];
    while rest.len() >= 4 {
        let n = u32::from_le_bytes([rest[0], rest[1], rest[2], rest[3]]) as usize;
        let Some(item) = rest.get(4..4 + n) else { break };
        rest = &rest[4 + n..];
        let line = match item.split_first() {
            Some((&op, arg)) => answer(op, &String::from_utf8_lossy(arg)),
            None => "BAD OP".into(),
        };
        if writeln!(out, "{line}").is_err() {
            return;
        }
    }
}
