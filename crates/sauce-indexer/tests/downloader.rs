use std::io::{Read, Write};
use std::net::TcpListener;
use std::thread;
use std::time::Duration;

use sauce_core::errors::SauceError;
use sauce_indexer::downloader::ImageDownloader;

fn serve_once(response: String) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());

    thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        let mut buffer = [0; 1024];
        let _ = stream.read(&mut buffer);
        stream.write_all(response.as_bytes()).unwrap();
    });

    url
}

#[tokio::test]
async fn rejects_image_when_content_length_is_too_large() {
    let length = 32 * 1024 * 1024 + 1;
    let url = serve_once(format!(
        "HTTP/1.1 200 OK\r\nContent-Length: {length}\r\nContent-Type: image/png\r\nConnection: close\r\n\r\n"
    ));
    let downloader = ImageDownloader::new("sauce-indexer-test", Duration::from_secs(2)).unwrap();

    let error = downloader.download(&url).await.unwrap_err();

    match error {
        SauceError::InvalidInput(message) => assert!(message.contains("image is too large")),
        other => panic!("unexpected error: {other}"),
    }
}
