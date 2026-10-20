//! Reads inputs from stdin, each a little-endian u32 length and that many bytes of JSON, and
//! prints one line per input: the input read as `Strict` and written back with
//! `serde_json::to_string_pretty` (newlines escaped as \n so it stays one line), or `ERROR`.
//! The Go test mirrors these types.

use std::collections::BTreeMap;
use std::io::{Read, Write};

use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize)]
struct Strict {
    a: u32,
    b: String,
    c: Option<f32>,
    d: Vec<i64>,
    e: [u8; 3],
    f: f64,
    g: Inner,
    h: BTreeMap<String, u32>,
    m: BTreeMap<u32, String>,
    #[serde(default)]
    x: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    o: Option<Box<Inner>>,
    bytes: Vec<u8>,
}

#[derive(Serialize, Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Inner {
    #[serde(skip_serializing_if = "Option::is_none")]
    size: Option<f32>,
    bold_cs: Option<bool>,
    font_name: Option<String>,
    list: Vec<f32>,
    nested: Vec<Inner>,
    n: i32,
}

fn main() {
    let mut input = Vec::new();
    std::io::stdin().read_to_end(&mut input).unwrap();
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    let mut rest = &input[..];
    while rest.len() >= 4 {
        let n = u32::from_le_bytes([rest[0], rest[1], rest[2], rest[3]]) as usize;
        let item = &rest[4..4 + n];
        rest = &rest[4 + n..];
        let line = match serde_json::from_slice::<Strict>(item) {
            Ok(v) => serde_json::to_string_pretty(&v).unwrap().replace('\\', "\\\\").replace('\n', "\\n"),
            Err(_) => "ERROR".to_string(),
        };
        writeln!(out, "{line}").unwrap();
    }
}
