mod wire;
use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq, Default)]
pub enum Color {
    #[default]
    Default,
    Indexed(u8),
    Rgb(u8, u8, u8),
}
impl From<vt100::Color> for Color {
    fn from(c: vt100::Color) -> Self {
        match c {
            vt100::Color::Default => Self::Default,
            vt100::Color::Idx(i) => Self::Indexed(i),
            vt100::Color::Rgb(r, g, b) => Self::Rgb(r, g, b),
        }
    }
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct Cell {
    pub text: String,
    pub fg: Color,
    pub bg: Color,
    pub bold: bool,
    pub dim: bool,
    pub italic: bool,
    pub underline: bool,
    pub inverse: bool,
    pub wide: bool,
    pub continuation: bool,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct Screen {
    pub cols: u16,
    pub rows: u16,
    #[serde(with = "wire")]
    pub cells: Vec<Vec<Cell>>,
    pub cursor: (u16, u16),
    pub hide_cursor: bool,
    pub application_cursor: bool,
    pub bracketed_paste: bool,
    pub mouse_sgr: bool,
    pub mouse_mode: u8,
    pub alternate: bool,
    pub scrollback: usize,
}
impl Screen {
    pub fn text(&self) -> String {
        self.cells
            .iter()
            .map(|r| {
                r.iter()
                    .filter(|c| !c.continuation)
                    .map(|c| if c.text.is_empty() { " " } else { &c.text })
                    .collect::<String>()
                    .trim_end()
                    .to_string()
            })
            .collect::<Vec<_>>()
            .join("\n")
    }
}
#[derive(Default)]
pub struct Callbacks {
    pub replies: Vec<u8>,
    pub notifications: Vec<String>,
}
impl vt100::Callbacks for Callbacks {
    fn audible_bell(&mut self, _: &mut vt100::Screen) {
        if self.notifications.len() < 64 {
            self.notifications.push("Terminal bell".into());
        }
    }
    fn unhandled_osc(&mut self, _: &mut vt100::Screen, params: &[&[u8]]) {
        if params.len() > 1
            && (params[0] == b"9" || (params[0] == b"777" && params[1] == b"notify"))
            && self.notifications.len() < 64
        {
            let skip = if params[0] == b"777" { 2 } else { 1 };
            let text = params
                .iter()
                .skip(skip)
                .map(|s| String::from_utf8_lossy(s).into_owned())
                .collect::<Vec<_>>()
                .join("; ");
            self.notifications.push(
                text.chars()
                    .filter(|c| !c.is_control())
                    .take(1024)
                    .collect(),
            );
        }
    }
    fn unhandled_csi(
        &mut self,
        screen: &mut vt100::Screen,
        i1: Option<u8>,
        _: Option<u8>,
        params: &[&[u16]],
        c: char,
    ) {
        if self.replies.len() > 4096 {
            return;
        }
        match (
            c,
            params.first().and_then(|s| s.first()).copied().unwrap_or(0),
        ) {
            ('n', 5) if i1.is_none() => self.replies.extend_from_slice(b"\x1b[0n"),
            ('n', 6) => {
                let (r, c) = screen.cursor_position();
                self.replies.extend_from_slice(
                    format!(
                        "\x1b[{}{};{}R",
                        if i1 == Some(b'?') { "?" } else { "" },
                        r + 1,
                        c + 1
                    )
                    .as_bytes(),
                );
            }
            ('c', _) => self.replies.extend_from_slice(if i1 == Some(b'>') {
                b"\x1b[>0;1;0c"
            } else {
                b"\x1b[?1;2c"
            }),
            _ => (),
        }
    }
}
pub struct Terminal {
    parser: vt100::Parser<Callbacks>,
}
impl Terminal {
    pub fn new(rows: u16, cols: u16, scrollback: usize) -> Self {
        Self {
            parser: vt100::Parser::new_with_callbacks(
                rows.max(1),
                cols.max(1),
                scrollback,
                Callbacks::default(),
            ),
        }
    }
    pub fn process(&mut self, bytes: &[u8]) {
        self.parser.process(bytes);
    }
    pub fn resize(&mut self, rows: u16, cols: u16) {
        self.parser.screen_mut().set_size(rows.max(1), cols.max(1));
    }
    pub fn events(&mut self) -> Callbacks {
        std::mem::take(self.parser.callbacks_mut())
    }
    /// Search retained physical lines without serializing the entire history.
    /// Offset zero includes the live screen; larger offsets visit older rows.
    pub fn search(&mut self, query: &str, offset: usize, backwards: bool) -> Option<usize> {
        if query.is_empty() {
            return None;
        }
        self.parser.screen_mut().set_scrollback(usize::MAX);
        let oldest = self.parser.screen().scrollback();
        if backwards && offset > oldest {
            self.parser.screen_mut().set_scrollback(0);
            return None;
        }
        let start = offset.min(oldest);
        let offsets: Box<dyn Iterator<Item = usize>> = if backwards {
            Box::new(start..=oldest)
        } else {
            Box::new((0..=start).rev())
        };
        let mut found = None;
        for at in offsets {
            self.parser.screen_mut().set_scrollback(at);
            let s = self.parser.screen();
            let cols = s.size().1;
            let matches = if at == 0 {
                s.rows(0, cols).any(|line| line.contains(query))
            } else {
                s.rows(0, cols)
                    .next()
                    .is_some_and(|line| line.contains(query))
            };
            if matches {
                found = Some(at);
                break;
            }
        }
        self.parser.screen_mut().set_scrollback(0);
        found
    }
    pub fn snapshot(&mut self, offset: usize) -> Screen {
        self.parser.screen_mut().set_scrollback(offset);
        let s = self.parser.screen();
        let (rows, cols) = s.size();
        let cells = (0..rows)
            .map(|r| {
                (0..cols)
                    .map(|c| {
                        let v = s.cell(r, c).expect("cell within screen");
                        Cell {
                            text: v.contents().into(),
                            fg: v.fgcolor().into(),
                            bg: v.bgcolor().into(),
                            bold: v.bold(),
                            dim: v.dim(),
                            italic: v.italic(),
                            underline: v.underline(),
                            inverse: v.inverse(),
                            wide: v.is_wide(),
                            continuation: v.is_wide_continuation(),
                        }
                    })
                    .collect()
            })
            .collect();
        let result = Screen {
            rows,
            cols,
            cells,
            cursor: s.cursor_position(),
            hide_cursor: s.hide_cursor(),
            application_cursor: s.application_cursor(),
            bracketed_paste: s.bracketed_paste(),
            mouse_sgr: s.mouse_protocol_encoding() == vt100::MouseProtocolEncoding::Sgr,
            mouse_mode: match s.mouse_protocol_mode() {
                vt100::MouseProtocolMode::None => 0,
                vt100::MouseProtocolMode::Press => 1,
                vt100::MouseProtocolMode::PressRelease => 2,
                vt100::MouseProtocolMode::ButtonMotion => 3,
                vt100::MouseProtocolMode::AnyMotion => 4,
            },
            alternate: s.alternate_screen(),
            scrollback: s.scrollback(),
        };
        self.parser.screen_mut().set_scrollback(0);
        result
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn search_visits_history_both_directions_without_moving_live_screen() {
        let mut t = Terminal::new(3, 30, 40);
        t.process(b"needle-old\r\n");
        for _ in 0..12 {
            t.process(b"other\r\n");
        }
        t.process(b"needle-new\r\nlast");
        let live = t.snapshot(0).text();
        assert_eq!(t.search("needle-new", 0, true), Some(0));
        let old = t.search("needle-old", 1, true).unwrap();
        assert!(old > 3);
        assert!(t.snapshot(old).text().contains("needle-old"));
        assert_eq!(t.search("needle-new", old - 1, false), Some(0));
        assert_eq!(t.search("absent", 0, true), None);
        assert_eq!(t.search("needle-old", usize::MAX, true), None);
        assert_eq!(t.snapshot(0).text(), live);
        t.process(b"\x1b[?1049hfull-screen");
        assert_eq!(t.search("needle-old", 0, true), None);
    }
    #[test]
    fn ansi_unicode_alternate_and_resize() {
        let mut t = Terminal::new(4, 20, 100);
        for b in "\x1b[31m界e\u{301}\x1b[0m".as_bytes() {
            t.process(&[*b]);
        }
        let s = t.snapshot(0);
        assert_eq!(s.cells[0][0].text, "界");
        assert!(s.cells[0][1].continuation);
        assert_eq!(s.cells[0][2].text, "e\u{301}");
        assert_eq!(s.cells[0][0].fg, Color::Indexed(1));
        t.process(b"\x1b[?1049hother\x1b[?1049l");
        assert!(t.snapshot(0).text().contains('界'));
        t.resize(8, 40);
        assert_eq!(t.snapshot(0).cols, 40);
    }
    #[test]
    fn notifications_and_device_queries() {
        let mut t = Terminal::new(4, 20, 10);
        t.process(b"\x1b]777;notify;Build;finished\x07\x1b[6n");
        let e = t.events();
        assert_eq!(e.notifications, vec!["Build; finished"]);
        assert_eq!(e.replies, b"\x1b[1;1R");
    }
    #[test]
    fn scrollback_is_bounded() {
        let mut t = Terminal::new(2, 10, 3);
        for _ in 0..20 {
            t.process(b"line\r\n");
        }
        assert_eq!(t.snapshot(100).scrollback, 3);
    }
}
