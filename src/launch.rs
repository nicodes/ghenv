use ghenv::provider::{Result, Values};
#[cfg(unix)]
use std::time::Duration;

#[cfg(unix)]
pub fn launch(args: &[std::ffi::OsString], environment: &Values, inherit: bool) -> Result<i32> {
    use std::{
        io,
        os::unix::process::CommandExt,
        process::Command,
        sync::{
            Arc,
            atomic::{AtomicI32, AtomicUsize, Ordering},
        },
        thread,
        time::Instant,
    };
    const GRACE: Duration = Duration::from_secs(5);
    struct Handlers(Vec<signal_hook::SigId>);
    impl Drop for Handlers {
        fn drop(&mut self) {
            for id in self.0.drain(..) {
                signal_hook::low_level::unregister(id);
            }
        }
    }
    struct Terminal {
        group: libc::pid_t,
        action: libc::sigaction,
    }
    impl Drop for Terminal {
        fn drop(&mut self) {
            unsafe {
                libc::tcsetpgrp(0, self.group);
                libc::sigaction(libc::SIGTTOU, &self.action, std::ptr::null_mut());
            }
        }
    }
    let last = Arc::new(AtomicI32::new(0));
    let count = Arc::new(AtomicUsize::new(0));
    let mut handlers = Handlers(Vec::new());
    for signal in [libc::SIGINT, libc::SIGTERM, libc::SIGHUP] {
        let last = last.clone();
        let count = count.clone();
        // The handler only stores lock-free atomics, with no allocation or I/O.
        handlers.0.push(unsafe {
            signal_hook::low_level::register(signal, move || {
                last.store(signal, Ordering::SeqCst);
                count.fetch_add(1, Ordering::SeqCst);
            })
        }?);
    }
    let foreground = unsafe { libc::tcgetpgrp(0) };
    let terminal = if foreground >= 0 {
        let mut old: libc::sigaction = unsafe { std::mem::zeroed() };
        let mut ignore: libc::sigaction = unsafe { std::mem::zeroed() };
        ignore.sa_sigaction = libc::SIG_IGN;
        unsafe {
            libc::sigemptyset(&mut ignore.sa_mask);
        }
        if unsafe { libc::sigaction(libc::SIGTTOU, &ignore, &mut old) } < 0 {
            return Err(io::Error::last_os_error().into());
        }
        Some(Terminal {
            group: foreground,
            action: old,
        })
    } else {
        None
    };
    let interactive = terminal.is_some();
    let mut command = Command::new(&args[0]);
    command
        .args(&args[1..])
        .env_clear()
        .envs(if inherit {
            std::env::vars_os().collect()
        } else {
            Vec::new()
        })
        .envs(environment)
        .process_group(0);
    unsafe {
        command.pre_exec(move || {
            if interactive {
                if libc::tcsetpgrp(0, libc::getpid()) < 0 {
                    return Err(io::Error::last_os_error());
                }
                libc::signal(libc::SIGTTOU, libc::SIG_DFL);
            }
            Ok(())
        });
    }
    let mut child = command.spawn()?;
    let group = child.id() as libc::pid_t;
    let mut deadline = None;
    let mut seen = 0;
    let mut killed = false;
    let status = loop {
        if let Some(status) = child.try_wait()? {
            break status;
        }
        let received = count.load(Ordering::SeqCst);
        if received > seen {
            if deadline.is_none() && received == 1 {
                unsafe {
                    libc::kill(-group, last.load(Ordering::SeqCst));
                }
                deadline = Some(Instant::now() + GRACE);
            } else {
                unsafe {
                    libc::kill(-group, libc::SIGKILL);
                }
                deadline = Some(Instant::now());
                killed = true;
            }
            seen = received;
        }
        if !killed && deadline.is_some_and(|end| Instant::now() >= end) {
            unsafe {
                libc::kill(-group, libc::SIGKILL);
            }
            killed = true;
        }
        thread::sleep(Duration::from_millis(10));
    };
    let end = deadline.unwrap_or_else(|| Instant::now() + GRACE);
    unsafe {
        libc::kill(-group, libc::SIGTERM);
    }
    loop {
        if unsafe { libc::kill(-group, 0) } < 0
            && io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH)
        {
            break;
        }
        if Instant::now() >= end || count.load(Ordering::SeqCst) > seen {
            unsafe {
                libc::kill(-group, libc::SIGKILL);
            }
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    drop(terminal);
    Ok(status.code().unwrap_or(1))
}
#[cfg(not(unix))]
pub fn launch(_args: &[std::ffi::OsString], _environment: &Values, _inherit: bool) -> Result<i32> {
    Err("ghenv currently supports process supervision on Linux and macOS only".into())
}
