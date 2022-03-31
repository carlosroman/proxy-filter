mod server;

use crate::server::ProxyClient;
use clap::Parser;
use std::net::SocketAddr;
use tracing::info;

#[derive(clap::Parser, Debug)]
#[clap(author, version, about, long_about = None)]
struct Args {
    // Base endpoint to send data to
    #[clap(short, long, default_value = "http://127.0.0.1:8080")]
    base_endpoint: String,
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt::init();
    let args = Args::parse();
    let addr = SocketAddr::from(([0, 0, 0, 0], 3000));
    let forward_addr = args.base_endpoint;
    info!("Starting server at '{}'", addr);

    let proxy_client = ProxyClient::new(
        addr,
        forward_addr,
        // SocketAddr::from(([192, 168, 64, 8], 8080)),
    );

    let server = server::new!(proxy_client);
    if let Err(e) = server.await {
        eprintln!("server error: {}", e);
    }
}
