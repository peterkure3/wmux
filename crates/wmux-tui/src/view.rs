use super::{App, Mode};
use ratatui::{
    Frame,
    layout::Rect,
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Clear, Paragraph, Wrap},
};
use std::collections::BTreeMap;
use wmux_core::{ProcessState, layout::Rect as CoreRect};

fn rgb(c: [u8; 3]) -> Color {
    Color::Rgb(c[0], c[1], c[2])
}
const DIM: Color = Color::Rgb(119, 135, 160);
const GREEN: Color = Color::Rgb(76, 225, 170);
const YELLOW: Color = Color::Rgb(247, 207, 80);
const BORDER: Color = Color::Rgb(68, 81, 107);
pub fn draw(f: &mut Frame, app: &App) -> Vec<(String, Rect)> {
    let area = f.area();
    let bg = rgb(app.config.theme.background);
    let fg = rgb(app.config.theme.foreground);
    let purple = rgb(app.config.theme.purple);
    let cyan = rgb(app.config.theme.cyan);
    f.render_widget(Block::default().style(Style::default().bg(bg).fg(fg)), area);
    if area.width < 20 || area.height < 5 {
        f.render_widget(
            Paragraph::new("wmux: terminal too small").style(Style::default().fg(YELLOW)),
            area,
        );
        return Vec::new();
    }
    let running = app
        .snapshot
        .state
        .workspaces
        .values()
        .flat_map(|w| w.panes.values())
        .filter(|p| p.state == ProcessState::Running)
        .count();
    let agents = app
        .snapshot
        .state
        .workspaces
        .values()
        .flat_map(|w| w.panes.values())
        .filter(|p| p.agent.is_some() && p.state == ProcessState::Running)
        .count();
    f.render_widget(
        Paragraph::new(Line::from(vec![
            Span::styled(" ◈ wmux ", Style::default().fg(purple).bold()),
            Span::raw(format!(
                " {}  /  {} processes · {} agents",
                env!("CARGO_PKG_VERSION"),
                running,
                agents
            )),
            Span::styled(
                if app.connected {
                    "  ● attached"
                } else {
                    "  ○ disconnected"
                },
                Style::default().fg(if app.connected { GREEN } else { YELLOW }),
            ),
        ])),
        Rect::new(area.x, area.y, area.width, 1),
    );
    let sidebar_width = if app.sidebar && area.width >= 90 {
        26
    } else {
        0
    };
    let body = Rect::new(area.x, area.y + 1, area.width, area.height - 2);
    if sidebar_width > 0 {
        sidebar(
            f,
            Rect::new(body.x, body.y, sidebar_width, body.height),
            app,
        );
    }
    let center = Rect::new(
        body.x + sidebar_width,
        body.y,
        body.width - sidebar_width,
        body.height,
    );
    let mut tabs = vec![Span::styled(" ", Style::default())];
    for (i, w) in app.snapshot.state.workspaces.values().enumerate() {
        tabs.push(Span::styled(
            format!(" {} {} ", i + 1, w.name),
            Style::default()
                .fg(if w.id == app.active { purple } else { DIM })
                .add_modifier(if w.id == app.active {
                    Modifier::BOLD
                } else {
                    Modifier::empty()
                }),
        ));
    }
    tabs.push(Span::styled(" + ", Style::default().fg(cyan)));
    f.render_widget(
        Paragraph::new(Line::from(tabs)),
        Rect::new(center.x, center.y, center.width, 1),
    );
    let content = Rect::new(
        center.x,
        center.y + 1,
        center.width,
        center.height.saturating_sub(1),
    );
    let lower_height = if app.lower && content.height >= 14 {
        app.lower_height.min(content.height / 2)
    } else {
        0
    };
    let upper = Rect::new(
        content.x,
        content.y,
        content.width,
        content.height - lower_height,
    );
    let inspector_width = if app.inspector && upper.width >= 90 {
        (upper.width / 3).min(50)
    } else {
        0
    };
    let panes_area = Rect::new(
        upper.x,
        upper.y,
        upper.width - inspector_width,
        upper.height,
    );
    if inspector_width > 0 {
        let inspector = Rect::new(panes_area.right(), upper.y, inspector_width, upper.height);
        f.render_widget(
            Paragraph::new(app.git_text.clone())
                .scroll((app.inspector_scroll, 0))
                .wrap(Wrap { trim: false })
                .block(
                    Block::default()
                        .title(" Git inspector ")
                        .borders(Borders::ALL)
                        .border_style(Style::default().fg(BORDER)),
                ),
            inspector,
        );
    }
    let focused = app.focused();
    let mut rects = Vec::new();
    if let Some(w) = app.workspace() {
        if w.zoomed || panes_area.width < 35 || panes_area.height < 10 {
            if let Some(id) = &focused {
                rects.push((id.clone(), panes_area));
            }
        } else if let Some(n) = &w.layout {
            rects = n
                .rects(CoreRect {
                    x: panes_area.x,
                    y: panes_area.y,
                    width: panes_area.width,
                    height: panes_area.height,
                })
                .into_iter()
                .map(|(id, r)| (id, Rect::new(r.x, r.y, r.width, r.height)))
                .collect();
        }
        // Render terminal cells first, then one merged border mesh. Shared cells
        // carry combined edge masks; intersections never leave blank gutters.
        let mut mesh: BTreeMap<(u16, u16), (u8, Color, u8)> = BTreeMap::new();
        for (id, r) in &rects {
            if r.width < 2 || r.height < 2 {
                continue;
            }
            let focus = focused.as_ref() == Some(id);
            let p = w.panes.get(id);
            let color = if focus {
                purple
            } else if p.is_some_and(|p| p.state == ProcessState::Running) {
                cyan
            } else {
                BORDER
            };
            let priority = if focus { 2 } else { 1 };
            edges(&mut mesh, *r, color, priority);
            if let Some(s) = app.screen(id) {
                for (y, row) in s
                    .cells
                    .iter()
                    .take(r.height.saturating_sub(2) as usize)
                    .enumerate()
                {
                    for (x, c) in row
                        .iter()
                        .take(r.width.saturating_sub(2) as usize)
                        .enumerate()
                    {
                        let target = &mut f.buffer_mut()[(r.x + 1 + x as u16, r.y + 1 + y as u16)];
                        if c.continuation {
                            target.set_symbol(" ");
                            continue;
                        }
                        // A double-width glyph at the last visible cell cannot
                        // overwrite the shared border.
                        let text = if c.text.is_empty()
                            || c.wide && x + 1 >= r.width.saturating_sub(2) as usize
                        {
                            " "
                        } else {
                            &c.text
                        };
                        let mut style = Style::default()
                            .fg(color_of(c.fg, fg))
                            .bg(color_of(c.bg, bg));
                        for (yes, modifier) in [
                            (c.bold, Modifier::BOLD),
                            (c.dim, Modifier::DIM),
                            (c.italic, Modifier::ITALIC),
                            (c.underline, Modifier::UNDERLINED),
                            (c.inverse, Modifier::REVERSED),
                        ] {
                            if yes {
                                style = style.add_modifier(modifier);
                            }
                        }
                        if !app.search.is_empty()
                            && row
                                .iter()
                                .map(|c| c.text.as_str())
                                .collect::<String>()
                                .contains(&app.search)
                        {
                            style = style.bg(Color::Rgb(60, 50, 10));
                        }
                        target.set_symbol(text).set_style(style);
                    }
                }
                if focus && app.mode == Mode::Insert && !s.hide_cursor && s.scrollback == 0 {
                    let (y, x) = s.cursor;
                    if x < r.width.saturating_sub(2) && y < r.height.saturating_sub(2) {
                        f.set_cursor_position((r.x + 1 + x, r.y + 1 + y));
                    }
                }
            } else {
                let text = p
                    .map(|p| {
                        format!(
                            "{:?}\nProcess is not connected. Split a new pane to start a shell.",
                            p.state
                        )
                    })
                    .unwrap_or_default();
                f.render_widget(
                    Paragraph::new(text).style(Style::default().fg(DIM)),
                    Rect::new(
                        r.x + 1,
                        r.y + 1,
                        r.width.saturating_sub(2),
                        r.height.saturating_sub(2),
                    ),
                );
            }
        }
        for ((x, y), (mask, color, _)) in mesh {
            f.buffer_mut()[(x, y)]
                .set_symbol(junction(mask))
                .set_style(Style::default().fg(color).bg(bg));
        }
        for (id, r) in &rects {
            if r.width < 6 || r.height < 2 {
                continue;
            }
            if let Some(p) = w.panes.get(id) {
                let title = format!(" {} · {:?} ", p.title, p.state);
                let color = if focused.as_ref() == Some(id) {
                    purple
                } else {
                    cyan
                };
                f.render_widget(
                    Paragraph::new(title).style(Style::default().fg(color)),
                    Rect::new(r.x + 2, r.y, r.width.saturating_sub(4), 1),
                );
            }
        }
        if rects.is_empty() {
            f.render_widget(
                Paragraph::new("No panes. Ctrl+B, V starts a shell.")
                    .style(Style::default().fg(DIM)),
                panes_area,
            );
        }
    } else {
        f.render_widget(
            Paragraph::new("No workspaces. Ctrl+B, N creates one.").style(Style::default().fg(DIM)),
            panes_area,
        );
    }
    if lower_height > 0 {
        lower(
            f,
            Rect::new(content.x, upper.bottom(), content.width, lower_height),
            app,
        );
    }
    let mode = match app.mode {
        Mode::Insert => {
            if app.read_only {
                "OBSERVE"
            } else {
                "INSERT"
            }
        }
        Mode::Command => "COMMAND",
        Mode::Copy | Mode::Search => "COPY",
        _ => "COMMAND",
    };
    let status = if app.message.is_empty() {
        format!(
            " {mode}  {}  {}  Ctrl+{} commands · Tab focus · ? help",
            app.workspace().map(|w| w.name.as_str()).unwrap_or(""),
            app.git_branch,
            app.config.prefix.to_ascii_uppercase()
        )
    } else {
        format!(" {mode}  {}", app.message)
    };
    f.render_widget(
        Paragraph::new(status).style(Style::default().fg(purple)),
        Rect::new(area.x, area.bottom() - 1, area.width, 1),
    );
    match app.mode {
        Mode::Palette => popup(
            f,
            area,
            "Command palette",
            super::PALETTE
                .iter()
                .enumerate()
                .map(|(i, (_, label))| {
                    format!(
                        "{} {}",
                        if i == app.palette_index { ">" } else { " " },
                        label
                    )
                })
                .collect(),
            purple,
        ),
        Mode::Help => popup(
            f,
            area,
            "Commands after prefix",
            vec![
                "Tab next pane · V columns · S rows · G grid".into(),
                "Z zoom · X close (confirm) · R rename · W swap".into(),
                "←/→ resize split · N new workspace · [/] previous/next".into(),
                "1–9 workspace · B sidebar · L lower panel · T panel view".into(),
                "I Git inspector, F file preview, prefix PgUp/PgDn scroll · +/- lower height · A approvals".into(),
                "C copy mode: PgUp/PgDn scroll · / search history, n/N next · Y copy".into(),
                "D or Q detach · prefix twice sends prefix to process".into(),
                "Press any key to return".into(),
            ],
            purple,
        ),
        Mode::Approvals => {
            let pending = app
                .snapshot
                .state
                .approvals
                .iter()
                .find(|a| a.status == wmux_core::ApprovalStatus::Pending);
            if let Some(a) = pending {
                popup(
                    f,
                    area,
                    if a.destructive {
                        "Destructive approval — explicit decision required"
                    } else {
                        "Approval request"
                    },
                    vec![
                        a.title.clone(),
                        a.details.clone(),
                        "Y approve · R reject · any other key inspect later".into(),
                    ],
                    YELLOW,
                );
            } else {
                popup(
                    f,
                    area,
                    "Approvals",
                    vec![
                        "No pending requests.".into(),
                        "Built-in agents use their native terminal approval prompts.".into(),
                    ],
                    YELLOW,
                );
            }
        }
        Mode::ConfirmClose => popup(
            f,
            area,
            "Close process pane?",
            vec![
                "This terminates the pane and its process group.".into(),
                "Y close · any other key cancel".into(),
            ],
            YELLOW,
        ),
        Mode::New | Mode::Rename | Mode::Search | Mode::Preview => popup(
            f,
            area,
            match app.mode {
                Mode::New => "New workspace",
                Mode::Rename => "Rename pane",
                Mode::Preview => "Preview file (repository-relative path)",
                _ => "Search retained history",
            },
            vec![app.prompt.clone(), "Enter confirm · Esc cancel".into()],
            purple,
        ),
        _ => (),
    }
    rects
}
fn color_of(c: wmux_terminal::Color, default: Color) -> Color {
    match c {
        wmux_terminal::Color::Default => default,
        wmux_terminal::Color::Indexed(i) => Color::Indexed(i),
        wmux_terminal::Color::Rgb(r, g, b) => Color::Rgb(r, g, b),
    }
}
fn sidebar(f: &mut Frame, r: Rect, app: &App) {
    let purple = rgb(app.config.theme.purple);
    f.render_widget(
        Block::default()
            .borders(Borders::RIGHT)
            .border_style(Style::default().fg(BORDER)),
        r,
    );
    f.render_widget(
        Paragraph::new(" WORKSPACES").style(Style::default().fg(purple).bold()),
        Rect::new(r.x, r.y, r.width - 1, 1),
    );
    let max_rows = (r.height.saturating_sub(7) / 2).max(1) as usize;
    let active_index = app
        .snapshot
        .state
        .workspaces
        .keys()
        .position(|id| id == &app.active)
        .unwrap_or(0);
    let start = active_index.saturating_sub(max_rows - 1);
    let mut y = r.y + 2;
    for (id, w) in app
        .snapshot
        .state
        .workspaces
        .iter()
        .skip(start)
        .take(max_rows)
    {
        if y + 1 >= r.bottom() {
            break;
        }
        let selected = id == &app.active;
        let pending = app
            .snapshot
            .state
            .notifications
            .iter()
            .filter(|n| n.workspace == *id)
            .count();
        f.render_widget(
            Paragraph::new(format!(" {} {}", if selected { "●" } else { "○" }, w.name)).style(
                Style::default()
                    .fg(if selected { purple } else { DIM })
                    .add_modifier(if selected {
                        Modifier::BOLD
                    } else {
                        Modifier::empty()
                    }),
            ),
            Rect::new(r.x, y, r.width - 1, 1),
        );
        f.render_widget(
            Paragraph::new(format!("   {} panes · {} notices", w.panes.len(), pending))
                .style(Style::default().fg(DIM)),
            Rect::new(r.x, y + 1, r.width - 1, 1),
        );
        y += 2;
    }
    if y + 3 < r.bottom() {
        y += 1;
        f.render_widget(
            Paragraph::new(" RECENT ACTIVITY").style(Style::default().fg(DIM)),
            Rect::new(r.x, y, r.width - 1, 1),
        );
        y += 1;
        for n in app
            .snapshot
            .state
            .notifications
            .iter()
            .rev()
            .take((r.bottom() - y).min(8) as usize)
        {
            f.render_widget(
                Paragraph::new(format!(" • {}", n.text)).style(Style::default().fg(YELLOW)),
                Rect::new(r.x, y, r.width - 1, 1),
            );
            y += 1;
        }
    }
}
fn lower(f: &mut Frame, r: Rect, app: &App) {
    let names = ["Logs", "Tasks", "Approvals", "Git", "Resources"];
    let title = names
        .iter()
        .enumerate()
        .map(|(i, n)| {
            if i == app.lower_tab {
                format!("[{n}]")
            } else {
                n.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join("  ");
    let text = match app.lower_tab {
        0 => app
            .snapshot
            .state
            .notifications
            .iter()
            .rev()
            .take(100)
            .map(|n| format!("{}  {}", n.created, n.text))
            .collect::<Vec<_>>()
            .join("\n"),
        1 => app
            .snapshot
            .state
            .tasks
            .iter()
            .filter(|t| t.workspace == app.active)
            .map(|t| format!("{}  {}  {}", &t.id[..8], t.status, t.title))
            .collect::<Vec<_>>()
            .join("\n"),
        2 => app
            .snapshot
            .state
            .approvals
            .iter()
            .rev()
            .map(|a| format!("{} {:?} {}\n{}", a.id, a.status, a.title, a.details))
            .collect::<Vec<_>>()
            .join("\n"),
        3 => app.git_text.clone(),
        _ => format!(
            "{} terminal buffers\n{} workspaces\nScrollback: {} lines per pane\nProcess limit: {}\nPTY output queue: 256 × up to 16 KiB\nIPC connection limit: 64",
            app.snapshot.screens.len(),
            app.snapshot.state.workspaces.len(),
            app.config.scrollback,
            app.config.max_panes
        ),
    };
    f.render_widget(
        Paragraph::new(text).wrap(Wrap { trim: false }).block(
            Block::default()
                .title(title)
                .borders(Borders::ALL)
                .border_style(Style::default().fg(BORDER)),
        ),
        r,
    );
}
fn popup(f: &mut Frame, area: Rect, title: &str, lines: Vec<String>, color: Color) {
    let width = area.width.saturating_sub(4).min(78);
    let height = (lines.len() as u16 + 2).min(area.height.saturating_sub(2));
    let r = Rect::new(
        area.x + (area.width - width) / 2,
        area.y + (area.height - height) / 2,
        width,
        height,
    );
    f.render_widget(Clear, r);
    f.render_widget(
        Paragraph::new(lines.join("\n"))
            .wrap(Wrap { trim: false })
            .block(
                Block::default()
                    .title(title)
                    .borders(Borders::ALL)
                    .border_style(Style::default().fg(color)),
            ),
        r,
    );
}
fn add(
    mesh: &mut BTreeMap<(u16, u16), (u8, Color, u8)>,
    x: u16,
    y: u16,
    mask: u8,
    color: Color,
    priority: u8,
) {
    let entry = mesh.entry((x, y)).or_insert((0, color, priority));
    entry.0 |= mask;
    if priority >= entry.2 {
        entry.1 = color;
        entry.2 = priority;
    }
}
fn edges(mesh: &mut BTreeMap<(u16, u16), (u8, Color, u8)>, r: Rect, color: Color, priority: u8) {
    // Bits: north=1 east=2 south=4 west=8.
    let right = r.right() - 1;
    let bottom = r.bottom() - 1;
    for x in r.x..right {
        add(mesh, x, r.y, 2, color, priority);
        add(mesh, x + 1, r.y, 8, color, priority);
        add(mesh, x, bottom, 2, color, priority);
        add(mesh, x + 1, bottom, 8, color, priority);
    }
    for y in r.y..bottom {
        add(mesh, r.x, y, 4, color, priority);
        add(mesh, r.x, y + 1, 1, color, priority);
        add(mesh, right, y, 4, color, priority);
        add(mesh, right, y + 1, 1, color, priority);
    }
}
fn junction(mask: u8) -> &'static str {
    match mask {
        3 => "└",
        6 => "┌",
        9 => "┘",
        12 => "┐",
        7 => "├",
        13 => "┤",
        11 => "┴",
        14 => "┬",
        15 => "┼",
        5 | 1 | 4 => "│",
        _ => "─",
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use wmux_core::{AgentActivity, Pane, Workspace, layout::Node};
    use wmux_ipc::Snapshot;
    fn app() -> App {
        let mut s = Snapshot::default();
        let ids: Vec<String> = ["a", "b", "c", "d"].into_iter().map(String::from).collect();
        let mut w = Workspace {
            id: "w".into(),
            name: "real-workspace".into(),
            cwd: ".".into(),
            panes: BTreeMap::new(),
            layout: Node::preset(&ids, "grid"),
            focused: Some("a".into()),
            zoomed: false,
        };
        for id in ids {
            w.panes.insert(
                id.clone(),
                Pane {
                    id,
                    title: "shell".into(),
                    command: vec![],
                    cwd: ".".into(),
                    state: ProcessState::Disconnected,
                    exit_code: None,
                    pid: None,
                    agent: None,
                    activity: AgentActivity::Unknown,
                },
            );
        }
        s.state.workspaces.insert("w".into(), w);
        App::new(s, None, wmux_core::config::Config::default(), false)
    }
    #[test]
    fn full_height_sidebar_and_connected_junctions() {
        for (width, height) in [(80, 24), (120, 40), (200, 60)] {
            let app = app();
            let mut t =
                ratatui::Terminal::new(ratatui::backend::TestBackend::new(width, height)).unwrap();
            let mut rects = vec![];
            t.draw(|f| rects = draw(f, &app)).unwrap();
            if width >= 90 {
                assert_eq!(t.backend().buffer()[(25, height - 2)].symbol(), "│");
            }
            let a = rects[0].1;
            let b = rects[1].1;
            let c = rects[2].1;
            assert_eq!(a.right() - 1, b.x);
            assert_eq!(a.bottom() - 1, c.y);
            assert_eq!(t.backend().buffer()[(b.x, c.y)].symbol(), "┼");
        }
    }
    #[test]
    fn focus_does_not_move_geometry_and_panels_collapse() {
        let mut app = app();
        let mut t = ratatui::Terminal::new(ratatui::backend::TestBackend::new(120, 40)).unwrap();
        let mut before = vec![];
        t.draw(|f| before = draw(f, &app)).unwrap();
        app.focused = Some("b".into());
        let mut after = vec![];
        t.draw(|f| after = draw(f, &app)).unwrap();
        assert_eq!(before, after);
        app.lower = true;
        t.draw(|f| after = draw(f, &app)).unwrap();
        assert!(after[2].1.bottom() < before[2].1.bottom());
        for (w, h) in [(1, 1), (19, 4), (40, 8)] {
            let mut t = ratatui::Terminal::new(ratatui::backend::TestBackend::new(w, h)).unwrap();
            t.draw(|f| {
                draw(f, &app);
            })
            .unwrap();
        }
    }
}
