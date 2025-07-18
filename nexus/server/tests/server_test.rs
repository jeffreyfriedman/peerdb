use std::{
    fs::{File, read_dir},
    io::{BufReader, Write, prelude::*},
    path::Path,
    process::Command,
    thread,
    time::Duration,
};

use postgres::{Client, NoTls, SimpleQueryMessage};
use similar::TextDiff;

mod create_peers;

fn input_files() -> Vec<String> {
    let sql_directory = read_dir("tests/sql").unwrap();
    sql_directory
        .filter_map(|sql_input| {
            sql_input.ok().and_then(|sql_file| {
                sql_file
                    .path()
                    .file_name()
                    .and_then(|n| n.to_str())
                    .filter(|n| n.ends_with(".sql"))
                    .map(String::from)
            })
        })
        .collect::<Vec<String>>()
}

fn setup_peers(client: &mut Client) {
    create_peers::create_bq::create(client);
    create_peers::create_pg::create(client);
    create_peers::create_sf::create(client);
}

fn read_queries(filename: impl AsRef<Path>) -> Vec<String> {
    let file = File::open(filename).expect("no such file");
    let buf = BufReader::new(file);
    buf.lines()
        .map(|l| l.expect("Could not parse line"))
        .collect()
}

struct PeerDBServer {
    server: std::process::Child,
}

impl PeerDBServer {
    fn new() -> Self {
        let mut server_start = Command::new("cargo");
        server_start.envs(std::env::vars());
        server_start.args(["run"]);
        tracing::info!("Starting server...");

        let f = File::create("server.log").expect("unable to open server.log");
        let child = server_start
            .stdout(std::process::Stdio::from(f))
            .spawn()
            .expect("Failed to start peerdb-server");

        thread::sleep(Duration::from_millis(5000));
        tracing::info!("peerdb-server Server started");
        Self { server: child }
    }

    fn connect_dying(&self) -> Client {
        let connection_string = "host=localhost port=9900 password=peerdb user=peerdb";
        let mut client_result = Client::connect(connection_string, NoTls);

        let mut client_established = false;
        let max_attempts = 10;
        let mut attempts = 0;
        while !client_established && attempts < max_attempts {
            match client_result {
                Ok(_) => {
                    client_established = true;
                }
                Err(_) => {
                    attempts += 1;
                    thread::sleep(Duration::from_millis(2000 * attempts));
                    client_result = Client::connect(connection_string, NoTls);
                }
            }
        }

        match client_result {
            Ok(c) => c,
            Err(_e) => {
                tracing::info!(
                    "unable to connect to server after {} attempts",
                    max_attempts
                );
                panic!("Failed to connect to server.");
            }
        }
    }
}

impl Drop for PeerDBServer {
    fn drop(&mut self) {
        tracing::info!("Stopping server...");
        self.server.kill().expect("Failed to kill peerdb-server");
        tracing::info!("Server stopped");
    }
}

#[test]
#[ignore = "create peers needs flow api"]
fn server_test() {
    let server = PeerDBServer::new();
    let mut client = server.connect_dying();
    setup_peers(&mut client);
    let test_files = input_files();
    test_files.iter().for_each(|file| {
        let queries = read_queries(["tests/sql/", file].concat());
        let actual_output_path = ["tests/results/actual/", file, ".out"].concat();
        let expected_output_path = ["tests/results/expected/", file, ".out"].concat();
        let mut output_file = File::create(["tests/results/actual/", file, ".out"].concat())
            .expect("Unable to create result file");

        for query in queries {
            dbg!(query.as_str());

            // filter out comments and empty lines
            if query.starts_with("--") || query.is_empty() {
                continue;
            }

            let res = client
                .simple_query(query.as_str())
                .expect("Failed to query");
            let mut column_names = Vec::new();
            if res.is_empty() {
                panic!("No results for query: {query}");
            }

            match res[0] {
                // Fetch column names for the output
                SimpleQueryMessage::Row(ref simplerow) => {
                    column_names.extend(simplerow.columns().iter().map(|column| column.name()));
                }
                SimpleQueryMessage::CommandComplete(_x) => (),
                _ => (),
            };

            let mut output = String::new();
            for row in res {
                if let SimpleQueryMessage::Row(simplerow) = row {
                    for idx in 0..simplerow.len() {
                        if let Some(val) = simplerow.get(idx) {
                            output.push_str(val);
                            output.push('\n');
                        }
                    }
                }
            }
            output_file
                .write_all(output.as_bytes())
                .expect("Unable to write query output");

            // flush the output file
            output_file.flush().expect("Unable to flush output file");
        }

        let obtained_file = std::fs::read_to_string(actual_output_path).unwrap();
        let expected_file = std::fs::read_to_string(expected_output_path).unwrap();
        // if there is a mismatch, print the diff, along with the path.
        if obtained_file != expected_file {
            tracing::info!("failed: {file}");
            let diff = TextDiff::from_lines(&expected_file, &obtained_file);
            for change in diff.iter_all_changes() {
                print!("{}{}", change.tag(), change);
            }

            panic!("result didn't match expected output");
        }
    });
}

#[test]
#[ignore = "create peers needs flow api"]
fn extended_query_protocol_no_params_catalog() {
    let server = PeerDBServer::new();
    let mut client = server.connect_dying();
    // create bigquery peer so that the following command returns a non-zero result
    create_peers::create_bq::create(&mut client);
    // run `select * from peers` as a prepared statement.
    let stmt = client
        .prepare("SELECT * FROM peers;")
        .expect("Failed to prepare query");

    // run the prepared statement with no parameters.
    let res = client
        .execute(&stmt, &[])
        .expect("Failed to execute prepared statement");

    // check that the result is non-empty.
    assert!(res > 0);
}

#[test]
fn query_unknown_peer_doesnt_crash_server() {
    let server = PeerDBServer::new();
    let mut client = server.connect_dying();

    // the server should not crash when a query is sent to an unknown peer.
    let query = "SELECT * FROM unknown_peer.test_table;";
    let res = client.simple_query(query);
    assert!(res.is_err());

    // assert that server is able to process a valid query after.
    let query = "SELECT * FROM peers;";
    let res = client.simple_query(query);
    assert!(res.is_ok());
}

#[test]
#[ignore = "requires some work for extended query prepares on bigquery."]
fn extended_query_protocol_no_params_bq() {
    let server = PeerDBServer::new();
    let mut client = server.connect_dying();

    let query = "SELECT country,count(*) from bq_test.users GROUP BY country;";

    // run `select * from peers` as a prepared statement.
    let stmt = client.prepare(query).expect("Failed to prepare query");

    // run the prepared statement with no parameters.
    let res = client
        .execute(&stmt, &[])
        .expect("Failed to execute prepared statement");

    // check that the result is non-empty.
    assert!(res > 0);
}
