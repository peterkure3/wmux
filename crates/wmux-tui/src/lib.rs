mod input;
mod view;
use anyhow::{Context, Result};
use crossterm::{
    event::{
        DisableBracketedPaste, DisableMouseCapture, EnableBracketedPaste, EnableMouseCapture,
        Event, EventStream, KeyCode, KeyEventKind, KeyModifiers, MouseButton, MouseEventKind,
    },
    execute,
    terminal::{EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode},
};
use futures_util::StreamExt;
use ratatui::{Terminal, backend::CrosstermBackend};
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    time::Duration,
};
use tokio::sync::{mpsc, watch};
use wmux_core::{Workspace, config::Config, layout::Axis};
use wmux_ipc::{Reply, Request, Snapshot};
use wmux_terminal::Screen;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Mode {
    Insert,
    Command,
    Help,
    Palette,
    ConfirmClose,
    Approvals,
    Copy,
    Search,
    Rename,
    New,
    Preview,
}
pub struct App {
    palette_index: usize,
    snapshot: Snapshot,
    active: String,
    mode: Mode,
    sidebar: bool,
    lower: bool,
    inspector: bool,
    lower_tab: usize,
    lower_height: u16,
    focused: Option<String>,
    message: String,
    connected: bool,
    read_only: bool,
    prompt: String,
    search: String,
    offset: usize,
    copy_screen: Option<Screen>,
    git_text: String,
    git_branch: String,
    preview_path: Option<String>,
    inspector_scroll: u16,
    config: Config,
}
impl App {
    pub fn new(
        snapshot: Snapshot,
        active: Option<String>,
        config: Config,
        read_only: bool,
    ) -> Self {
        let active = active
            .and_then(|s| {
                snapshot
                    .state
                    .workspaces
                    .values()
                    .find(|w| w.id == s || w.name == s)
                    .map(|w| w.id.clone())
            })
            .or_else(|| snapshot.state.workspaces.keys().next().cloned())
            .unwrap_or_default();
        let focused = snapshot
            .state
            .workspaces
            .get(&active)
            .and_then(|w| w.focused.clone());
        Self {
            palette_index: 0,
            snapshot,
            active,
            mode: Mode::Insert,
            sidebar: config.sidebar,
            lower: false,
            inspector: false,
            lower_tab: 0,
            lower_height: 8,
            focused,
            message: String::new(),
            connected: true,
            read_only,
            prompt: String::new(),
            search: String::new(),
            offset: 0,
            copy_screen: None,
            git_text: String::new(),
            git_branch: String::new(),
            preview_path: None,
            inspector_scroll: 0,
            config,
        }
    }
    fn workspace(&self) -> Option<&Workspace> {
        self.snapshot.state.workspaces.get(&self.active)
    }
    fn focused(&self) -> Option<String> {
        self.focused
            .clone()
            .filter(|id| self.workspace().is_some_and(|w| w.panes.contains_key(id)))
            .or_else(|| self.workspace().and_then(|w| w.focused.clone()))
    }
    fn screen(&self, id: &str) -> Option<&Screen> {
        if self.focused().as_deref() == Some(id) && self.copy_screen.is_some() {
            self.copy_screen.as_ref()
        } else {
            self.snapshot.screens.get(id)
        }
    }
    fn switch(&mut self, delta: isize) {
        let ids: Vec<_> = self.snapshot.state.workspaces.keys().cloned().collect();
        if ids.is_empty() {
            return;
        }
        let index = ids.iter().position(|id| id == &self.active).unwrap_or(0);
        self.active = ids[(index as isize + delta).rem_euclid(ids.len() as isize) as usize].clone();
        self.focused = self.workspace().and_then(|w| w.focused.clone());
        self.copy_screen = None;
        self.offset = 0;
        self.mode = Mode::Insert;
        self.preview_path = None;
        self.inspector_scroll = 0;
    }
}
struct Guard;
impl Drop for Guard {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
        let _ = execute!(
            std::io::stdout(),
            DisableMouseCapture,
            DisableBracketedPaste,
            LeaveAlternateScreen,
            crossterm::cursor::Show
        );
    }
}
fn dispatch(tx: &mpsc::Sender<Request>, app: &mut App, req: Request) {
    if app.read_only {
        app.message = "Read-only observer: process controls disabled".into();
        return;
    }
    if let Err(e) = tx.try_send(req) {
        app.message = format!("Command queue: {e}");
    }
}
async fn copy_view(home: &Path, app: &mut App) -> Result<()> {
    if let Some(pane) = app.focused() {
        let mut offsets = BTreeMap::new();
        offsets.insert(pane.clone(), app.offset);
        if let Reply::Snapshot(mut s) = wmux_ipc::call(
            home,
            Request::Get {
                scrollback: offsets,
            },
        )
        .await?
        {
            app.copy_screen = s.screens.remove(&pane);
            app.mode = Mode::Copy;
        }
    }
    Ok(())
}
async fn search_history(home: &Path, app: &mut App, backwards: bool, repeat: bool) -> Result<()> {
    if let Some(pane) = app.focused() {
        let offset = if !repeat {
            app.offset
        } else if backwards {
            app.offset.saturating_add(1)
        } else {
            app.offset.saturating_sub(1)
        };
        if let Reply::Search(found) = wmux_ipc::call(
            home,
            Request::Search {
                pane,
                query: app.search.clone(),
                offset,
                backwards,
            },
        )
        .await?
        {
            if let Some(offset) = found {
                app.offset = offset;
                copy_view(home, app).await?;
                app.message = format!("History match at offset {offset}; n older, N newer");
            } else {
                app.message = "No further history match".into();
            }
        }
    }
    Ok(())
}
pub async fn run(
    home: PathBuf,
    config: Config,
    active: Option<String>,
    read_only: bool,
) -> Result<()> {
    let Reply::Snapshot(snapshot) = wmux_ipc::call(
        &home,
        Request::Get {
            scrollback: BTreeMap::new(),
        },
    )
    .await?
    else {
        anyhow::bail!("invalid initial state");
    };
    let mut app = App::new(snapshot, active, config, read_only);
    enable_raw_mode().context("WMUX requires an interactive terminal")?;
    let _guard = Guard;
    execute!(
        std::io::stdout(),
        EnterAlternateScreen,
        EnableMouseCapture,
        EnableBracketedPaste
    )?;
    let mut terminal = Terminal::new(CrosstermBackend::new(std::io::stdout()))?;
    terminal.clear()?;
    let (updates, mut update_rx) = watch::channel::<Option<Snapshot>>(None);
    let (errors, mut error_rx) = watch::channel(String::new());
    let subscribe_home = home.clone();
    let subscribe_errors = errors.clone();
    let subscriber = tokio::spawn(async move {
        loop {
            let result: Result<()> = async {
                let mut s = wmux_ipc::connect(&subscribe_home).await?;
                wmux_ipc::write(
                    &mut s,
                    &wmux_ipc::Envelope {
                        version: wmux_ipc::VERSION,
                        request: Request::Subscribe,
                    },
                )
                .await?;
                loop {
                    match wmux_ipc::read::<_, wmux_ipc::Response>(&mut s)
                        .await?
                        .result()?
                    {
                        Reply::Snapshot(s) => {
                            updates.send_replace(Some(s));
                        }
                        _ => anyhow::bail!("unexpected subscription response"),
                    }
                }
            }
            .await;
            if let Err(e) = result {
                subscribe_errors.send_replace(format!("Disconnected: {e}"));
            }
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
    });
    let (tx, mut rx) = mpsc::channel::<Request>(128);
    let command_home = home.clone();
    let commands = tokio::spawn(async move {
        let mut control = None;
        while let Some(req) = rx.recv().await {
            if control.is_none() {
                match wmux_ipc::Control::connect(&command_home).await {
                    Ok(client) => control = Some(client),
                    Err(e) => {
                        errors.send_replace(e.to_string());
                        continue;
                    }
                }
            }
            if let Err(e) = control.as_mut().unwrap().call(req).await {
                errors.send_replace(e.to_string());
                // Never retry input: a lost reply may follow a successful write.
                // Keep the live connection on application errors; it owns leases.
                if !control.as_ref().unwrap().is_connected() {
                    control = None;
                }
            }
        }
    });
    let (git_tx, mut git_rx) = watch::channel((String::new(), String::new()));
    let (cwd_tx, mut cwd_rx) = watch::channel((
        app.workspace().map(|w| w.cwd.clone()).unwrap_or_default(),
        None::<String>,
    ));
    let git = tokio::spawn(async move {
        loop {
            let (cwd, preview) = cwd_rx.borrow().clone();
            let (text, branch) = tokio::task::spawn_blocking(move || {
                let path = Path::new(&cwd);
                match wmux_git::info(path) {
                    Ok(info) => {
                        if let Some(preview) = preview {
                            let text = wmux_git::preview(path, Path::new(&preview))
                                .map(|p| format!("{}\n{}{}", p.path, wmux_security::clean_text(&p.text), if p.truncated {"\n[truncated at 64 KiB]"} else {""}))
                                .unwrap_or_else(|e| format!("Preview unavailable: {e}"));
                            return (text, info.branch);
                        }
                        let diff = wmux_git::diff(path).unwrap_or_else(|e| e.to_string());
                        let staged = wmux_git::staged_diff(path).unwrap_or_else(|e| e.to_string());
                        let untracked = wmux_git::untracked(path).map(|files| files.join("\n")).unwrap_or_else(|e| e.to_string());
                        (
                            format!(
                                "{}\n{} changed files · {} worktrees\n\n{}",
                                info.root,
                                info.changed,
                                info.worktrees,
                                wmux_security::clean_text(&format!("Working tree\n{diff}\nStaged\n{staged}\nUntracked files\n{untracked}"))
                            ),
                            info.branch,
                        )
                    }
                    Err(e) => (format!("Git unavailable: {e}"), String::new()),
                }
            })
            .await
            .unwrap_or_else(|e| (format!("Git worker failed: {e}"), String::new()));
            git_tx.send_replace((text, branch));
            tokio::select! {_=tokio::time::sleep(Duration::from_secs(5))=>(),r=cwd_rx.changed()=>if r.is_err(){break;}}
        }
    });
    let result:Result<()>=async {
        let mut events=EventStream::new();let mut sizes=BTreeMap::<String,(u16,u16)>::new();
        loop {
            let mut rects=Vec::new();
            terminal.draw(|f|{rects=view::draw(f,&app);})?;
            if app.mode!=Mode::Copy&&app.mode!=Mode::Search&&!app.read_only{
                for (id,r) in &rects {
                    let size=(r.width.saturating_sub(2).clamp(1,400),r.height.saturating_sub(2).clamp(1,150));
                    if sizes.get(id)!=Some(&size)&&app.snapshot.screens.contains_key(id)
                        && tx.try_send(Request::Resize{pane:id.clone(),cols:size.0,rows:size.1}).is_ok(){sizes.insert(id.clone(),size);}
                }
            }
            tokio::select! {
                r=update_rx.changed()=>{
                    r?;if let Some(snapshot)=update_rx.borrow_and_update().clone(){
                        let previous_focus=app.workspace().and_then(|w|w.focused.clone());
                        let new_workspace=snapshot.state.workspaces.keys().find(|id|!app.snapshot.state.workspaces.contains_key(*id)).cloned();
                        app.snapshot=snapshot;app.connected=true;
                        if let Some(id)=new_workspace {app.active=id;}
                        let next_focus=app.workspace().and_then(|w|w.focused.clone());
                        if next_focus!=previous_focus && !app.read_only {app.focused=next_focus;}
                        if !app.snapshot.state.workspaces.contains_key(&app.active){app.active=app.snapshot.state.workspaces.keys().next().cloned().unwrap_or_default();}
                        if app.focused().is_none(){app.focused=app.workspace().and_then(|w|w.focused.clone());}
                    }
                },
                r=error_rx.changed()=>{r?;app.message=error_rx.borrow_and_update().clone();if app.message.starts_with("Disconnected:"){app.connected=false;sizes.clear();}},
                r=git_rx.changed()=>{r?;let (text,branch)=git_rx.borrow_and_update().clone();app.git_text=text;app.git_branch=branch;},
                event=events.next()=>{
                    let Some(event)=event else{break;};let event=event?;
                    match event {
                        Event::Resize(..)=>(),
                        Event::Paste(text) if app.mode==Mode::Insert=>{
                            if let Some(pane)=app.focused(){
                                let bracketed=app.screen(&pane).is_some_and(|s|s.bracketed_paste);
                                let text=text.replace("\x1b[201~","");
                                // Explicit paste is bounded and chunked; wrapper is never interleaved by this client.
                                if text.len()>60_000{app.message="Paste exceeds 60 KiB".into();}
                                else {let data=if bracketed{format!("\x1b[200~{text}\x1b[201~").into_bytes()}else{text.into_bytes()};dispatch(&tx,&mut app,Request::Input{pane,data});}
                            }
                        },
                        Event::Mouse(m)=>{
                            if let Some((id,r))=rects.iter().find(|(_,r)|m.column>r.x&&m.column<r.right().saturating_sub(1)&&m.row>r.y&&m.row<r.bottom().saturating_sub(1)){
                                let id=id.clone();let r=*r;
                                if matches!(m.kind,MouseEventKind::Down(MouseButton::Left)){app.focused=Some(id.clone());}
                                if let Some(s)=app.screen(&id){
                                    if s.mouse_mode>0&&s.mouse_sgr {
                                        let code=match m.kind{
                                            MouseEventKind::Down(MouseButton::Left)=>Some((0,'M')),MouseEventKind::Down(MouseButton::Middle)=>Some((1,'M')),MouseEventKind::Down(MouseButton::Right)=>Some((2,'M')),
                                            MouseEventKind::Up(MouseButton::Left)=>Some((0,'m')),MouseEventKind::Up(MouseButton::Middle)=>Some((1,'m')),MouseEventKind::Up(MouseButton::Right)=>Some((2,'m')),
                                            MouseEventKind::ScrollUp=>Some((64,'M')),MouseEventKind::ScrollDown=>Some((65,'M')),
                                            MouseEventKind::Drag(MouseButton::Left) if s.mouse_mode>=3=>Some((32,'M')),
                                            MouseEventKind::Moved if s.mouse_mode==4=>Some((35,'M')),_=>None,
                                        };
                                        if let Some((code,end))=code{
                                            let data=format!("\x1b[<{code};{};{}{end}",m.column-r.x,m.row-r.y).into_bytes();
                                            dispatch(&tx,&mut app,Request::Input{pane:id,data});
                                        }
                                    }else if matches!(m.kind,MouseEventKind::ScrollUp|MouseEventKind::ScrollDown){
                                        app.offset=if m.kind==MouseEventKind::ScrollUp{app.offset.saturating_add(3)}else{app.offset.saturating_sub(3)};
                                        if let Err(e)=copy_view(&home,&mut app).await{app.message=e.to_string();}
                                    }
                                }
                            }else if matches!(m.kind,MouseEventKind::Down(MouseButton::Left))&&app.sidebar&&m.column<26 {
                                let index=m.row.saturating_sub(3) as usize/2;
                                if let Some(id)=app.snapshot.state.workspaces.keys().nth(index){app.active=id.clone();app.focused=app.workspace().and_then(|w|w.focused.clone());}
                            }
                        },
                        Event::Key(mut k) if k.kind!=KeyEventKind::Release=>{
                            if k.code==KeyCode::Char(app.config.prefix)&&k.modifiers.contains(KeyModifiers::CONTROL){
                                if app.mode==Mode::Command {
                                    if let Some(pane)=app.focused(){let data=input::encode(k,false);dispatch(&tx,&mut app,Request::Input{pane,data});}
                                    app.mode=Mode::Insert;
                                }else{app.mode=Mode::Command;}
                                continue;
                            }
                            if matches!(app.mode,Mode::Rename|Mode::New|Mode::Search|Mode::Preview){
                                match k.code {
                                    KeyCode::Esc=>app.mode=if app.copy_screen.is_some(){Mode::Copy}else{Mode::Insert},
                                    KeyCode::Backspace=>{app.prompt.pop();},
                                    KeyCode::Char(c) if !k.modifiers.contains(KeyModifiers::CONTROL)&&app.prompt.len()<512=>app.prompt.push(c),
                                    KeyCode::Enter=>{
                                        match app.mode{
                                            Mode::Rename=>if let Some(pane)=app.focused(){let req=Request::Rename{workspace:app.active.clone(),pane:Some(pane),title:app.prompt.clone()};dispatch(&tx,&mut app,req);},
                                            Mode::New=>{let req=Request::New{name:app.prompt.clone(),cwd:app.workspace().map(|w|w.cwd.clone()).unwrap_or_else(||".".into()),command:Vec::new()};dispatch(&tx,&mut app,req);},
                                            Mode::Preview=>{app.preview_path=Some(app.prompt.clone());app.inspector=true;app.inspector_scroll=0;},
                                            Mode::Search=>{app.search=app.prompt.clone();app.mode=Mode::Copy;if let Err(e)=search_history(&home,&mut app,true,false).await{app.message=e.to_string();}continue;},_=>(),
                                        }app.mode=Mode::Insert;
                                    },_=>(),
                                }continue;
                            }

                            if app.mode==Mode::Palette {
                                match k.code {
                                    KeyCode::Up=>{app.palette_index=app.palette_index.saturating_sub(1);continue;},
                                    KeyCode::Down=>{app.palette_index=(app.palette_index+1).min(PALETTE.len()-1);continue;},
                                    KeyCode::Enter=>{
                                        let action=PALETTE[app.palette_index].0;
                                        k.code=app.config.keybindings.get(action).and_then(|s|s.chars().next()).map(KeyCode::Char).unwrap_or(KeyCode::Null);
                                        app.mode=Mode::Command;
                                    },
                                    _=>{app.mode=Mode::Insert;continue;},
                                }
                            }
                            if app.mode==Mode::Approvals {
                                if let Some(a)=app.snapshot.state.approvals.iter().find(|a|a.status==wmux_core::ApprovalStatus::Pending) {
                                    let id=a.id.clone();
                                    if k.code==KeyCode::Char('y') || k.code==KeyCode::Char('r') {
                                        dispatch(&tx,&mut app,Request::ApprovalDecide{id,approve:k.code==KeyCode::Char('y')});
                                    }
                                }
                                app.mode=Mode::Insert;continue;
                            }
                            if app.mode==Mode::Help {app.mode=Mode::Insert;continue;}
                            if app.mode==Mode::ConfirmClose {
                                if k.code==KeyCode::Char('y')&& let Some(pane)=app.focused(){let req=Request::Close{workspace:app.active.clone(),pane};dispatch(&tx,&mut app,req);}
                                app.mode=Mode::Insert;continue;
                            }
                            if app.mode==Mode::Copy {
                                match k.code {
                                    KeyCode::Esc|KeyCode::Char('q')=>{app.mode=Mode::Insert;app.offset=0;app.copy_screen=None;app.search.clear();},
                                    KeyCode::PageUp|KeyCode::Up=>{app.offset=app.offset.saturating_add(if k.code==KeyCode::Up{1}else{20});if let Err(e)=copy_view(&home,&mut app).await{app.message=e.to_string();}},
                                    KeyCode::PageDown|KeyCode::Down=>{app.offset=app.offset.saturating_sub(if k.code==KeyCode::Down{1}else{20});if let Err(e)=copy_view(&home,&mut app).await{app.message=e.to_string();}},
                                    KeyCode::Char('/')=>{app.prompt.clear();app.mode=Mode::Search;},
                                    KeyCode::Char('n')|KeyCode::Char('N')=>{if let Err(e)=search_history(&home,&mut app,k.code==KeyCode::Char('n'),true).await{app.message=e.to_string();}},
                                    KeyCode::Char('y')=>{
                                        if let Some(s)=&app.copy_screen{
                                            use base64::Engine;
                                            let data=base64::engine::general_purpose::STANDARD.encode(s.text());
                                            use std::io::Write;
                                            write!(std::io::stdout(),"\x1b]52;c;{data}\x07")?;std::io::stdout().flush()?;
                                            app.message="Visible buffer copied with OSC 52 (terminal support required)".into();
                                        }
                                    },_=>(),
                                }continue;
                            }
                            if app.mode==Mode::Command {
                                app.mode=Mode::Insert;let pane=app.focused();
                                let code=if let KeyCode::Char('1'..='9')=k.code {k.code}else if let KeyCode::Char(c)=k.code {
                                    let action=app.config.keybindings.iter().find(|(_,key)|key.as_str()==c.to_string()).map(|(a,_)|a);
                                    action.and_then(|a|wmux_core::config::default_bindings().get(a).and_then(|s|s.chars().next())).map(KeyCode::Char).unwrap_or(KeyCode::Null)
                                }else{k.code};
                                match code {
                                    KeyCode::Char('q')|KeyCode::Char('d')=>break,
                                    KeyCode::Char('b')=>app.sidebar = !app.sidebar,
                                    KeyCode::Char('l')=>app.lower = !app.lower,
                                    KeyCode::Char('i')=>{if app.preview_path.take().is_some(){app.inspector=true;app.inspector_scroll=0;}else{app.inspector = !app.inspector;}},
                                    KeyCode::Char('f')=>{app.prompt.clear();app.mode=Mode::Preview;},
                                    KeyCode::PageDown=>app.inspector_scroll=app.inspector_scroll.saturating_add(10),
                                    KeyCode::PageUp=>app.inspector_scroll=app.inspector_scroll.saturating_sub(10),
                                    KeyCode::Char('a')=>{app.lower=true;app.lower_tab=2;app.mode=Mode::Approvals;},
                                    KeyCode::Char('t')=>{app.lower=true;app.lower_tab=(app.lower_tab+1)%5;},
                                    KeyCode::Char('+')=>app.lower_height=(app.lower_height+2).min(24),
                                    KeyCode::Char('-')=>app.lower_height=app.lower_height.saturating_sub(2).max(4),
                                    KeyCode::Char('p')=>app.mode=Mode::Palette,
                                    KeyCode::Char('?')=>app.mode=Mode::Help,
                                    KeyCode::Char('n')=>{app.prompt.clear();app.mode=Mode::New;},
                                    KeyCode::Char('r')=>{app.prompt.clear();app.mode=Mode::Rename;},
                                    KeyCode::Char('[')=>app.switch(-1),KeyCode::Char(']')=>app.switch(1),
                                    KeyCode::Char(c @ '1'..='9')=>if let Some(id)=app.snapshot.state.workspaces.keys().nth(c as usize-'1' as usize){app.active=id.clone();app.focused=app.workspace().and_then(|w|w.focused.clone());},
                                    KeyCode::Char('s')|KeyCode::Char('v')=>{
                                        let req=Request::Split{workspace:app.active.clone(),pane,axis:if code==KeyCode::Char('s'){Axis::Rows}else{Axis::Columns},command:Vec::new(),agent:None};
                                        dispatch(&tx,&mut app,req);
                                    },
                                    KeyCode::Char('z')=>{let req=Request::Zoom{workspace:app.active.clone()};dispatch(&tx,&mut app,req);},
                                    KeyCode::Char('g')=>{let req=Request::Layout{workspace:app.active.clone(),name:"grid".into()};dispatch(&tx,&mut app,req);},
                                    KeyCode::Char('x')=>app.mode=Mode::ConfirmClose,
                                    KeyCode::Char('c')=>if let Err(e)=copy_view(&home,&mut app).await{app.message=e.to_string();},
                                    KeyCode::Left|KeyCode::Right=>if let Some(pane)=pane{
                                        let req=Request::Adjust{workspace:app.active.clone(),pane,delta:if k.code==KeyCode::Left{-50}else{50}};dispatch(&tx,&mut app,req);
                                    },
                                    KeyCode::Tab|KeyCode::Char('w')=>{
                                        let ids=app.workspace().and_then(|w|w.layout.as_ref()).map(|n|n.leaves()).unwrap_or_default();
                                        if !ids.is_empty(){
                                            let i=ids.iter().position(|id|Some(id)==pane.as_ref()).unwrap_or(0);let next=ids[(i+1)%ids.len()].clone();
                                            if code==KeyCode::Char('w'){
                                                if let Some(first)=pane{let req=Request::Swap{workspace:app.active.clone(),first,second:next};dispatch(&tx,&mut app,req);}
                                            }else{app.focused=Some(next.clone());let req=Request::Focus{workspace:app.active.clone(),pane:next};dispatch(&tx,&mut app,req);}
                                        }
                                    },_=>(),
                                }
                            }else if let Some(pane)=app.focused(){
                                let data=input::encode(k,app.screen(&pane).is_some_and(|s|s.application_cursor));
                                if !data.is_empty(){dispatch(&tx,&mut app,Request::Input{pane,data});}
                            }
                        },_=>(),
                    }
                    let cwd=(app.workspace().map(|w|w.cwd.clone()).unwrap_or_default(), app.preview_path.clone());
                    if *cwd_tx.borrow()!=cwd {cwd_tx.send_replace(cwd);}
                }
            }
        }Ok(())
    }.await;
    subscriber.abort();
    commands.abort();
    git.abort();
    result
}
/// Headless rendering uses the same view and real daemon screen cells as the interactive UI.
pub fn render_buffer(
    snapshot: Snapshot,
    active: Option<String>,
    config: Config,
    width: u16,
    height: u16,
) -> Result<ratatui::buffer::Buffer> {
    let app = App::new(snapshot, active, config, false);
    let mut terminal = Terminal::new(ratatui::backend::TestBackend::new(width, height))?;
    terminal.draw(|f| {
        view::draw(f, &app);
    })?;
    Ok(terminal.backend().buffer().clone())
}

const PALETTE: [(&str, &str); 9] = [
    ("new", "New workspace"),
    ("split_vertical", "Split into columns"),
    ("split_horizontal", "Split into rows"),
    ("grid", "Arrange grid"),
    ("zoom", "Zoom pane"),
    ("copy", "Copy and search buffer"),
    ("approvals", "Inspect approval queue"),
    ("sidebar", "Toggle sidebar"),
    ("detach", "Detach UI"),
];
