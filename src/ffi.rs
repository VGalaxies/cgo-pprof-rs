use futures::{
    future::BoxFuture,
    task::{Context, Poll},
    Future, FutureExt,
};
use hyper::{
    service::{make_service_fn, service_fn},
    Body, Request, Response, Server, StatusCode,
};
use lazy_static::lazy_static;
use pprof::protos::Message;
use regex::Regex;
use std::{collections::HashMap, pin::Pin, sync::Mutex, time::Duration};
use std::{convert::Infallible, thread};
use tokio::runtime::{Builder, Runtime};

static mut RT: *mut Runtime = std::ptr::null_mut();

#[no_mangle]
pub unsafe extern "C" fn init_tokio_runtime() {
    let tokio_rt = Builder::new_multi_thread()
        .thread_name("ffi")
        .worker_threads(4)
        .enable_all()
        .build()
        .unwrap();
    tokio_rt.block_on(async {
        println!("Tokio runtime initialized");
    });
    let tokio_rt = Box::new(tokio_rt);
    unsafe {
        RT = Box::into_raw(tokio_rt);
    }

    spawn_http_server();
}

fn make_response<T>(status_code: StatusCode, message: T) -> Response<Body>
where
    T: Into<Body>,
{
    Response::builder()
        .status(status_code)
        .body(message.into())
        .unwrap()
}

pub async fn dump_cpu_prof_to_resp(req: Request<Body>) -> hyper::Result<Response<Body>> {
    let query = req.uri().query().unwrap_or("");
    let query_pairs: HashMap<_, _> = url::form_urlencoded::parse(query.as_bytes()).collect();

    let seconds: u64 = match query_pairs.get("seconds") {
        Some(val) => match val.parse() {
            Ok(val) => val,
            Err(err) => return Ok(make_response(StatusCode::BAD_REQUEST, err.to_string())),
        },
        None => 10,
    };

    let frequency: i32 = match query_pairs.get("frequency") {
        Some(val) => match val.parse() {
            Ok(val) => val,
            Err(err) => return Ok(make_response(StatusCode::BAD_REQUEST, err.to_string())),
        },
        None => 99, /* Default frequency of sampling. 99Hz to avoid coincide with special
                     * periods */
    };

    let prototype_content_type: hyper::http::HeaderValue =
        hyper::http::HeaderValue::from_str("application/protobuf").unwrap();
    let output_protobuf = req.headers().get("Content-Type") == Some(&prototype_content_type);

    let timer = tokio::time::sleep(Duration::from_secs(seconds));
    let end = async move {
        timer.await;
        Ok(())
    };

    match start_one_cpu_profile(end, frequency, output_protobuf).await {
        Ok(body) => {
            dbg!("dump cpu profile successfully");
            let mut response = Response::builder()
                .header(
                    "Content-Disposition",
                    "attachment; filename=\"cpu_profile\"",
                )
                .header("Content-Length", body.len());
            response = if output_protobuf {
                response.header("Content-Type", mime::APPLICATION_OCTET_STREAM.to_string())
            } else {
                response.header("Content-Type", mime::IMAGE_SVG.to_string())
            };
            Ok(response.body(body.into()).unwrap())
        }
        Err(e) => {
            dbg!("dump cpu profile fail: {}", e.clone());
            Ok(make_response(StatusCode::INTERNAL_SERVER_ERROR, e))
        }
    }
}

lazy_static! {
    // If it's some it means there are already a CPU profiling.
    static ref CPU_PROFILE_ACTIVE: Mutex<Option<()>> = Mutex::new(None);

    // To normalize thread names.
    static ref THREAD_NAME_RE: Regex =
        Regex::new(r"^(?P<thread_name>[a-z-_ :]+?)(-?\d)*$").unwrap();
    static ref THREAD_NAME_REPLACE_SEPERATOR_RE: Regex = Regex::new(r"[_ ]").unwrap();
}

/// Simulates Go's defer.
///
/// Please note that, different from go, this defer is bound to scope.
/// When exiting the scope, its deferred calls are executed in last-in-first-out
/// order.
#[macro_export]
macro_rules! defer {
    ($t:expr) => {
        let __ctx = DeferContext::new(|| $t);
    };
}

/// Invokes the wrapped closure when dropped.
pub struct DeferContext<T: FnOnce()> {
    t: Option<T>,
}

impl<T: FnOnce()> DeferContext<T> {
    pub fn new(t: T) -> DeferContext<T> {
        DeferContext { t: Some(t) }
    }
}

impl<T: FnOnce()> Drop for DeferContext<T> {
    fn drop(&mut self) {
        self.t.take().unwrap()()
    }
}

fn extract_thread_name(thread_name: &str) -> String {
    THREAD_NAME_RE
        .captures(thread_name)
        .and_then(|cap| {
            cap.name("thread_name").map(|thread_name| {
                THREAD_NAME_REPLACE_SEPERATOR_RE
                    .replace_all(thread_name.as_str(), "-")
                    .into_owned()
            })
        })
        .unwrap_or_else(|| thread_name.to_owned())
}

/// Trigger one cpu profile.
pub async fn start_one_cpu_profile<F>(
    end: F,
    frequency: i32,
    protobuf: bool,
) -> Result<Vec<u8>, String>
where
    F: Future<Output = Result<(), String>> + Send + 'static,
{
    if CPU_PROFILE_ACTIVE.lock().unwrap().is_some() {
        return Err("Already in CPU Profiling".to_owned());
    }

    let on_start = || {
        let mut activate = CPU_PROFILE_ACTIVE.lock().unwrap();
        assert!(activate.is_none());
        *activate = Some(());
        let guard = pprof::ProfilerGuardBuilder::default()
            .frequency(frequency)
            .blocklist(&["libc", "libgcc", "pthread", "vdso"])
            .build()
            .map_err(|e| format!("pprof::ProfilerGuardBuilder::build fail: {}", e))?;
        Ok(guard)
    };

    let on_end = move |guard: pprof::ProfilerGuard<'static>| {
        defer! {
            *CPU_PROFILE_ACTIVE.lock().unwrap() = None
        }
        let report = guard
            .report()
            .frames_post_processor(move |frames| {
                let name = extract_thread_name(&frames.thread_name);
                frames.thread_name = name;
            })
            .build()
            .map_err(|e| format!("create cpu profiling report fail: {}", e))?;
        let mut body = Vec::new();
        if protobuf {
            let profile = report
                .pprof()
                .map_err(|e| format!("generate pprof from report fail: {}", e))?;
            profile
                .write_to_vec(&mut body)
                .map_err(|e| format!("encode pprof into bytes fail: {}", e))?;
        } else {
            report
                .flamegraph(&mut body)
                .map_err(|e| format!("generate flamegraph from report fail: {}", e))?;
        }
        drop(guard);

        Ok(body)
    };

    ProfileRunner::new(on_start, on_end, end.boxed())?.await
}

type OnEndFn<I, T> = Box<dyn FnOnce(I) -> Result<T, String> + Send + 'static>;

struct ProfileRunner<I, T> {
    item: Option<I>,
    on_end: Option<OnEndFn<I, T>>,
    end: BoxFuture<'static, Result<(), String>>,
}

impl<I, T> Unpin for ProfileRunner<I, T> {}

impl<I, T> ProfileRunner<I, T> {
    fn new<F1, F2>(
        on_start: F1,
        on_end: F2,
        end: BoxFuture<'static, Result<(), String>>,
    ) -> Result<Self, String>
    where
        F1: FnOnce() -> Result<I, String>,
        F2: FnOnce(I) -> Result<T, String> + Send + 'static,
    {
        let item = on_start()?;
        Ok(ProfileRunner {
            item: Some(item),
            on_end: Some(Box::new(on_end) as OnEndFn<I, T>),
            end,
        })
    }
}

impl<I, T> Future for ProfileRunner<I, T> {
    type Output = Result<T, String>;
    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        match self.end.as_mut().poll(cx) {
            Poll::Ready(res) => {
                let item = self.item.take().unwrap();
                let on_end = self.on_end.take().unwrap();
                let r = match (res, on_end(item)) {
                    (Ok(_), r) => r,
                    (Err(errmsg), _) => Err(errmsg),
                };
                Poll::Ready(r)
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

async fn handle_req(req: Request<Body>) -> Result<Response<Body>, Infallible> {
    match req.uri().path() {
        "/debug/pprof/profile" => {
            let response = dump_cpu_prof_to_resp(req).await.unwrap_or_else(|e| {
                make_response(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    format!("Hyper internal error: {}", e),
                )
            });
            Ok(response)
        }
        _ => {
            panic!("not found")
        }
    }
}

fn spawn_http_server() {
    thread::spawn(|| {
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("failed to build HTTP server runtime");

        rt.block_on(async {
            let addr = ([0, 0, 0, 0], 9096).into();
            let make_svc =
                make_service_fn(|_conn| async { Ok::<_, Infallible>(service_fn(handle_req)) });

            println!("Debug HTTP server listening on http://{}", addr);
            Server::bind(&addr)
                .serve(make_svc)
                .await
                .expect("HTTP server error");
        });
    });
}
