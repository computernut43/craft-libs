// Reference program for docpath/rust_diff_test.go: build with `cargo build --release` here.
// Reads `raw<TAB>doc` lines, each hex-encoded UTF-8, and prints `Kind<TAB>ok:<hex path>` or
// `Kind<TAB>err:<Refused>` per line (an empty doc means no document).
use std::io::{BufRead, Write};
use std::path::Path;

fn unescape(s: &str) -> String {
    let bytes: Vec<u8> = (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap()).collect();
    String::from_utf8(bytes).unwrap()
}

fn main() {
    let stdin = std::io::stdin();
    let mut out = std::io::BufWriter::new(std::io::stdout());
    for line in stdin.lock().lines() {
        let line = line.unwrap();
        let (r, d) = line.split_once('\t').unwrap();
        let raw = unescape(r);
        let doc = unescape(d);
        let kind = format!("{:?}", craft_docpath::classify(&raw));
        let res = match craft_docpath::resolve(&raw, if doc.is_empty() { None } else { Some(Path::new(&doc)) }) {
            Ok(p) => format!("ok:{}", p.to_str().unwrap().bytes().map(|b| format!("{b:02x}")).collect::<String>()),
            Err(e) => format!("err:{e:?}"),
        };
        writeln!(out, "{kind}\t{res}").unwrap();
    }
}
