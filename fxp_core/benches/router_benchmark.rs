// SPDX-License-Identifier: MIT

use criterion::{BenchmarkId, Criterion, criterion_group, criterion_main};
use fxp_core::server::bench_support::RouterBenchmark;
use std::time::Instant;

fn router_benchmark(criterion: &mut Criterion) {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(8)
        .enable_all()
        .build()
        .unwrap();
    let mut group = criterion.benchmark_group("symbol_router_get_or_create");
    group.sample_size(10);

    for symbol_count in [25, 1_000] {
        let router = runtime.block_on(RouterBenchmark::new(symbol_count));
        for task_count in [1, 8, 64] {
            group.bench_with_input(
                BenchmarkId::new(format!("{symbol_count}_symbols"), task_count),
                &task_count,
                |bencher, task_count| {
                    bencher.iter_custom(|iterations| {
                        let start = Instant::now();
                        runtime.block_on(async {
                            for _ in 0..iterations {
                                router.concurrent_lookups(*task_count, 32).await;
                            }
                        });
                        start.elapsed()
                    });
                },
            );
        }
    }

    group.finish();
}

criterion_group!(benches, router_benchmark);
criterion_main!(benches);
