use hyper::{client::HttpConnector, Body, Client, Request, Response, StatusCode};
use hyper_openssl::HttpsConnector;
use std::{convert::Infallible, net::SocketAddr, sync::Arc};

#[derive(Clone, Debug)]
pub struct ProxyClient {
    addr: SocketAddr,
    forward_addr: String,
    http_client: Client<HttpsConnector<HttpConnector>>,
}

impl ProxyClient {
    pub fn new(addr: SocketAddr, forward_addr: String) -> ProxyClient {
        let ssl = HttpsConnector::new().unwrap();
        let http_client = Client::builder().build::<_, Body>(ssl);
        ProxyClient {
            addr,
            forward_addr,
            http_client,
        }
    }
    pub fn addr(&self) -> SocketAddr {
        self.addr
    }
}

#[macro_export]
macro_rules! new {
    ($e:expr) => {{
        use crate::server::handle;
        use hyper::{
            service::{make_service_fn, service_fn},
            Server,
        };
        use std::{convert::Infallible, sync::Arc};

        let proxy_client: Arc<ProxyClient> = Arc::new($e);
        let proxy_addr = proxy_client.addr();
        let new_service = make_service_fn(move |_conn| {
            let proxy_client = Arc::clone(&proxy_client);
            let svc = service_fn(move |req| {
                // Clone again to ensure that client outlives this closure.
                let proxy_client = Arc::clone(&proxy_client);
                handle(req, proxy_client)
            });
            async move { Ok::<_, Infallible>(svc) }
        });
        let builder = Server::bind(&proxy_addr);
        builder.serve(new_service)
    }};
}

pub(crate) use new;

pub async fn handle(
    req: Request<Body>,
    proxy: Arc<ProxyClient>,
) -> Result<Response<Body>, Infallible> {
    let uri_string = if let Some(path_query) = req.uri().path_and_query() {
        format!("{}{}", proxy.forward_addr, path_query)
    } else {
        proxy.forward_addr.clone()
    };
    tracing::info!("uri_string: {}", uri_string);
    let uri = uri_string
        .parse::<hyper::Uri>()
        .expect("proxy addr should parse");
    let mut http_req_builder = Request::builder();
    {
        let headers = http_req_builder.headers_mut().unwrap();
        for (key, value) in req.headers() {
            tracing::info!("Sending: {}: {}", key, value.to_str().unwrap_or("NO VALUE"));
            headers.append(key, value.into());
        }
    }
    let http_req = http_req_builder
        .method(req.method())
        .uri(uri)
        .body(req.into_body());

    match http_req {
        Err(_) => {
            let mut response = Response::new(Body::empty());
            *response.status_mut() = StatusCode::BAD_REQUEST;
            Ok(response)
        }
        Ok(http_req) => {
            let http_resp = proxy.http_client.request(http_req).await.unwrap();
            let status_code = http_resp.status();
            tracing::info!("Sent request to {}, response {}", uri_string, status_code);
            let mut response_builder = Response::builder().status(status_code);
            {
                let headers = response_builder.headers_mut().unwrap();
                for (key, value) in http_resp.headers() {
                    headers.append(key, value.into());
                }
            }
            let response = response_builder.body(http_resp.into_body()).unwrap();
            Ok(response)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use futures_channel::oneshot;
    use hyper::{body::HttpBody, Client, Method, Request};
    use mockito::{mock, server_address, Matcher};
    use std::{
        borrow::Borrow,
        net::{SocketAddr, TcpListener as StdTcpListener},
        str, thread,
    };
    use tokio::net::TcpListener;

    #[tokio::test]
    async fn test_proxy_handle() {
        let mock = mock("POST", "/some/test/path")
            .match_body("{expected payload}")
            .match_header("content-type", "application/test")
            .match_header("DD-API-KEY", "SECRET API KEY")
            .match_header("user-agent", "unit-test")
            .with_body("{expected response}")
            .with_status(418)
            .expect(1)
            .create();
        let server = TestServer::serve(server_address());
        std::thread::sleep(std::time::Duration::from_secs(1));
        let uri_string = format!("http://{}/some/test/path", server.addr);
        let uri = uri_string
            .parse::<hyper::Uri>()
            .expect("server addr should parse");
        let client = Client::new();
        let req = Request::builder()
            .method(Method::POST)
            .header("content-type", "application/test")
            .header("DD-API-KEY", "SECRET API KEY")
            .header("user-agent", "unit-test")
            .uri(uri)
            .body(Body::from("{expected payload}"))
            .expect("request builder");
        let mut resp = client.request(req).await.unwrap();
        assert_eq!(resp.status(), 418);

        let body = resp.data().await;
        assert!(body.is_some(), "body must not be empty");
        assert_eq!(
            str::from_utf8(body.unwrap().unwrap().borrow()),
            Ok("{expected response}")
        );
        mock.assert();
    }

    #[tokio::test]
    async fn test_proxy_handle_get_request() {
        let mock = mock("GET", "/some/test/path")
            .match_header("user-agent", "unit-test")
            .match_query(Matcher::AllOf(vec![
                Matcher::UrlEncoded("key_one".into(), "value_one".into()),
                Matcher::UrlEncoded("key_two".into(), "value_two".into()),
            ]))
            .with_body("{expected response}")
            .with_status(418)
            .expect(1)
            .create();
        let server = TestServer::serve(server_address());
        std::thread::sleep(std::time::Duration::from_secs(1));
        let uri_string = format!(
            "http://{}/some/test/path?key_one=value_one&key_two=value_two",
            server.addr
        );
        let uri = uri_string
            .parse::<hyper::Uri>()
            .expect("server addr should parse");
        let client = Client::new();
        let req = Request::builder()
            .method(Method::GET)
            .header("user-agent", "unit-test")
            .uri(uri)
            .body(Body::empty())
            .expect("request builder");
        let mut resp = client.request(req).await.unwrap();
        assert_eq!(resp.status(), 418);

        let body = resp.data().await;
        assert!(body.is_some(), "body must not be empty");
        assert_eq!(
            str::from_utf8(body.unwrap().unwrap().borrow()),
            Ok("{expected response}")
        );
        mock.assert();
    }

    pub fn runtime() -> tokio::runtime::Runtime {
        tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("new rt")
    }

    fn tcp_bind(addr: &SocketAddr) -> ::tokio::io::Result<TcpListener> {
        let std_listener = StdTcpListener::bind(addr).unwrap();
        std_listener.set_nonblocking(true).unwrap();
        TcpListener::from_std(std_listener)
    }

    struct TestServer {
        addr: SocketAddr,
        _shutdown_signal: Option<oneshot::Sender<()>>,
        _thread: Option<thread::JoinHandle<()>>,
    }

    impl TestServer {
        fn serve(proxy_addr: SocketAddr) -> TestServer {
            let (shutdown_tx, shutdown_rx) = oneshot::channel();
            let listener = tcp_bind(&"127.0.0.1:0".parse().unwrap()).unwrap();
            let addr = listener.local_addr().unwrap();
            let thread = thread::Builder::new()
                .spawn(move || {
                    runtime()
                        .block_on(async move {
                            let proxy_client =
                                ProxyClient::new(addr, format!("http://{}", proxy_addr));
                            let server = new!(proxy_client);
                            server
                                .with_graceful_shutdown(async {
                                    let _ = shutdown_rx.await;
                                })
                                .await
                        })
                        .expect("serve()");
                })
                .expect("thread spawn");
            return TestServer {
                _shutdown_signal: Some(shutdown_tx),
                _thread: Some(thread),
                addr,
            };
        }
    }
}
