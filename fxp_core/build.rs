fn main() {
    prost_build::Config::new()
        .compile_protos(&["proto/fxp.proto"], &["proto"])
        .unwrap();
}
