use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
pub fn encode(k: KeyEvent, application: bool) -> Vec<u8> {
    let ctrl = k.modifiers.contains(KeyModifiers::CONTROL);
    let alt = k.modifiers.contains(KeyModifiers::ALT);
    let modifier = 1
        + u8::from(k.modifiers.contains(KeyModifiers::SHIFT))
        + 2 * u8::from(alt)
        + 4 * u8::from(ctrl);
    let mut s = match k.code {
        KeyCode::Char(c) if ctrl && c.is_ascii() => vec![(c.to_ascii_uppercase() as u8) & 0x1f],
        KeyCode::Char(c) => c.to_string().into_bytes(),
        KeyCode::Enter => vec![b'\r'],
        KeyCode::Backspace => vec![127],
        KeyCode::Tab => vec![b'\t'],
        KeyCode::Esc => vec![27],
        KeyCode::BackTab => b"\x1b[Z".to_vec(),
        KeyCode::Up
        | KeyCode::Down
        | KeyCode::Right
        | KeyCode::Left
        | KeyCode::Home
        | KeyCode::End => {
            let code = match k.code {
                KeyCode::Up => 'A',
                KeyCode::Down => 'B',
                KeyCode::Right => 'C',
                KeyCode::Left => 'D',
                KeyCode::Home => 'H',
                _ => 'F',
            };
            if modifier > 1 {
                format!("\x1b[1;{modifier}{code}").into_bytes()
            } else {
                format!("\x1b{}{code}", if application { "O" } else { "[" }).into_bytes()
            }
        }
        KeyCode::Insert | KeyCode::Delete | KeyCode::PageUp | KeyCode::PageDown => {
            let n = match k.code {
                KeyCode::Insert => 2,
                KeyCode::Delete => 3,
                KeyCode::PageUp => 5,
                _ => 6,
            };
            if modifier > 1 {
                format!("\x1b[{n};{modifier}~").into_bytes()
            } else {
                format!("\x1b[{n}~").into_bytes()
            }
        }
        KeyCode::F(n) if (1..=4).contains(&n) => {
            let c = (b'P' + n - 1) as char;
            if modifier > 1 {
                format!("\x1b[1;{modifier}{c}").into_bytes()
            } else {
                format!("\x1bO{c}").into_bytes()
            }
        }
        KeyCode::F(n) if (5..=12).contains(&n) => {
            let code = [15, 17, 18, 19, 20, 21, 23, 24][(n - 5) as usize];
            if modifier > 1 {
                format!("\x1b[{code};{modifier}~").into_bytes()
            } else {
                format!("\x1b[{code}~").into_bytes()
            }
        }
        _ => Vec::new(),
    };
    if alt
        && matches!(
            k.code,
            KeyCode::Char(_) | KeyCode::Enter | KeyCode::Backspace
        )
    {
        s.insert(0, 27);
    }
    s
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn input_preserves_agent_keys() {
        assert_eq!(
            encode(
                KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL),
                false
            ),
            vec![3]
        );
        assert_eq!(
            encode(KeyEvent::new(KeyCode::Up, KeyModifiers::NONE), true),
            b"\x1bOA"
        );
        assert_eq!(
            encode(KeyEvent::new(KeyCode::Left, KeyModifiers::CONTROL), false),
            b"\x1b[1;5D"
        );
        assert_eq!(
            encode(
                KeyEvent::new(KeyCode::Char('界'), KeyModifiers::NONE),
                false
            ),
            "界".as_bytes()
        );
    }
}
