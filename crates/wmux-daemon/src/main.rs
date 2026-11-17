#[tokio::main(worker_threads = 2)]
async fn main() -> anyhow::Result<()> {
    let home = wmux_core::config::home();
    wmux_security::private_dir(&home)?;
    let config = wmux_core::config::Config::load(&home)?;
    let log_path = home.join("daemon.log");
    let log = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)?;
    wmux_security::private_file(&log_path)?;
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| config.log_level.clone().into()),
        )
        .with_writer(std::sync::Arc::new(log))
        .init();
    let result = wmux_daemon::serve(home, config).await;
    if let Err(ref e) = result {
        tracing::error!(error=%e,"daemon stopped");
    }
    result
}
