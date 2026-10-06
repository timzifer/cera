//! Bench worker for hayro (see bench/protocol.md).

use hayro::hayro_interpret::InterpreterSettings;
use hayro::hayro_syntax::Pdf;
use hayro::vello_cpu::color::palette::css::WHITE;
use hayro::{PixmapSettings, RenderCache, RenderSettings, render};
use std::io::{BufRead, Write};
use std::time::Instant;

fn main() {
    let stdin = std::io::stdin();
    let mut lines = stdin.lock().lines();
    let mut out = std::io::stdout().lock();
    let mut pending: Option<String> = None;
    loop {
        let line = match pending.take() {
            Some(l) => l,
            None => match lines.next() {
                Some(Ok(l)) => l,
                _ => return,
            },
        };
        let f: Vec<&str> = line.split('\t').collect();
        match f[0] {
            "quit" => return,
            "version" => reply(&mut out, &["ok".into(), "hayro 0.8.0".into()]),
            "open" => {
                let data = match std::fs::read(f[1]) {
                    Ok(d) => d,
                    Err(e) => {
                        reply(&mut out, &err(&e.to_string()));
                        continue;
                    }
                };
                let password = f.get(2).copied().unwrap_or("");
                let t0 = Instant::now();
                let pdf = Pdf::new_with_password(data, password);
                let ns = t0.elapsed().as_nanos();
                match pdf {
                    Ok(pdf) => {
                        let n = pdf.pages().len();
                        reply(&mut out, &["ok".into(), ns.to_string(), n.to_string()]);
                        pending = serve(&pdf, &mut lines, &mut out);
                        if pending.is_none() {
                            return;
                        }
                    }
                    Err(e) => reply(&mut out, &err(&format!("{e:?}"))),
                }
            }
            _ => reply(&mut out, &["unsupported".into()]),
        }
    }
}

/// serve answers requests on an open document until the next `open`,
/// which it returns, or the end (None).
fn serve(
    pdf: &Pdf,
    lines: &mut impl Iterator<Item = std::io::Result<String>>,
    out: &mut impl Write,
) -> Option<String> {
    // One cache per document, reused across renders, as hayro advises.
    let cache = RenderCache::new();
    let settings = InterpreterSettings::default();
    let render_settings = RenderSettings::default();
    loop {
        let line = match lines.next() {
            Some(Ok(l)) => l,
            _ => return None,
        };
        let f: Vec<&str> = line.split('\t').collect();
        match f[0] {
            "quit" => return None,
            "open" => return Some(line),
            "version" => reply(out, &["ok".into(), "hayro 0.8.0".into()]),
            "render" => {
                let i: usize = f[1].parse().unwrap_or(0);
                let scale: f32 = f[2].parse().unwrap_or(1.0);
                let want_ink = f.get(3) == Some(&"1");
                let pages = pdf.pages();
                if i >= pages.len() {
                    reply(out, &err("no such page"));
                    continue;
                }
                let t0 = Instant::now();
                let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    render(
                        &pages[i],
                        &cache,
                        &settings,
                        &render_settings,
                        &PixmapSettings { x_scale: scale, y_scale: scale, bg_color: WHITE },
                    )
                }));
                let ns = t0.elapsed().as_nanos();
                match result {
                    Ok(pix) => {
                        let mut r = vec!["ok".to_string(), ns.to_string(), pix.width().to_string(), pix.height().to_string()];
                        if want_ink {
                            r.push(format!("{:.5}", ink(pix.data_as_u8_slice())));
                        }
                        reply(out, &r);
                    }
                    Err(_) => reply(out, &err("panic")),
                }
            }
            _ => reply(out, &["unsupported".into()]),
        }
    }
}

/// The share of colour channel values below white (RGBA, alpha skipped).
fn ink(px: &[u8]) -> f64 {
    let mut n = 0usize;
    for p in px.chunks_exact(4) {
        n += p[..3].iter().filter(|&&v| v != 255).count();
    }
    if px.is_empty() { 0.0 } else { n as f64 / (px.len() / 4 * 3) as f64 }
}

fn err(msg: &str) -> Vec<String> {
    vec!["err".into(), msg.replace(['\t', '\n'], " ")]
}

fn reply(out: &mut impl Write, fields: &[String]) {
    let _ = writeln!(out, "{}", fields.join("\t"));
    let _ = out.flush();
}
