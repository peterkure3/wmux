//! Real PTY echo fixture used only by the measurement harness.
use std::io::{self, BufRead, Write};
fn main() -> io::Result<()> {
    println!("WMUX_PROBE_READY");
    io::stdout().flush()?;
    for line in io::stdin().lock().lines() {
        let line = line?;
        if line == "quit" {
            break;
        }
        println!("WMUX_PROBE_{line}");
        io::stdout().flush()?;
    }
    Ok(())
}
