"""Sandbox-local, single-turn fork server. Parent never receives run configuration."""
import array
import json
import os
import signal
import socket
import sys

ROOT = '/sandbox/.hermes-warm'
SOCKET = ROOT + '/socket'
HOME = ROOT + '/home'


def client(ping=False):
    with socket.socket(socket.AF_UNIX, socket.SOCK_SEQPACKET) as sock:
        sock.connect(SOCKET)
        payload = json.dumps({'ping': True} if ping else {'env': dict(os.environ), 'cwd': os.getcwd()}).encode()
        if len(payload) > 65536:
            raise ValueError('warm environment too large')
        rights = [] if ping else [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array('i', [0, 1, 2]))]
        sock.sendmsg([payload], rights)
        sock.shutdown(socket.SHUT_WR)
        status = sock.recv(32)
        return int(status) if status else 1


def serve():
    os.makedirs(HOME, mode=0o700, exist_ok=True)
    # Imports can cache home paths. Every fork uses this same slot-local path;
    # the host fills it only after assignment and replaces it before each turn.
    os.environ.clear()
    os.environ.update(PATH='/usr/local/bin:/usr/bin:/bin', HOME=HOME,
                      HERMES_HOME=HOME, HERMES_DISABLE_LAZY_INSTALLS='1',
                      HERMES_ACP_SKIP_CONFIGURED_MCP='1')
    import hermes_bootstrap
    hermes_bootstrap.harden_import_path()
    from run_agent import AIAgent  # noqa: F401 — common code, no instance
    import model_tools, toolsets  # noqa: F401
    with socket.socket(socket.AF_UNIX, socket.SOCK_SEQPACKET) as server:
        server.bind(SOCKET)
        os.chmod(SOCKET, 0o600)
        server.listen(1)
        server.settimeout(int(sys.argv[2]))
        print('__openneko_warm_ready__', flush=True)
        while True:
            try:
                conn, _ = server.accept()
            except TimeoutError:
                return
            # Read configuration ONLY in the fork. The clean parent cannot
            # retain a previous turn's credentials, MCP registry or context.
            pid = os.fork()
            if pid == 0:
                server.close()
                os.setsid()
                try:
                    conn.settimeout(10)
                    data, anc, flags, _ = conn.recvmsg(65536, socket.CMSG_SPACE(12))
                    fds = array.array('i')
                    for level, kind, value in anc:
                        if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
                            fds.frombytes(value[:len(value) - len(value) % fds.itemsize])
                    request = json.loads(data)
                    if request == {'ping': True} and not fds:
                        os._exit(75)
                    if len(fds) != 3 or flags & (socket.MSG_TRUNC | socket.MSG_CTRUNC):
                        raise ValueError('invalid warm request')
                    env = request['env']
                    if env.get('HERMES_HOME') != HOME:
                        raise ValueError('warm home mismatch')
                    os.environ.clear()
                    os.environ.update(env)
                    os.chdir(request['cwd'])
                    for target, fd in enumerate(fds):
                        os.dup2(fd, target)
                        if fd > 2:
                            os.close(fd)
                    conn.close()
                    sys.argv = ['hermes', '--yolo', 'acp']
                    from hermes_cli.main import main
                    main()
                except SystemExit as error:
                    os._exit(error.code if isinstance(error.code, int) else 1)
                except BaseException:
                    import traceback
                    traceback.print_exc()
                    os._exit(1)
                os._exit(0)
            _, status = os.waitpid(pid, 0)
            code = os.waitstatus_to_exitcode(status)
            # Checkout renews the lease before policy sync/upload, which can
            # take longer than a deliberately short idle timeout in tests.
            server.settimeout(max(180, int(sys.argv[2])) if code == 75 else int(sys.argv[2]))
            try:
                os.killpg(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                conn.sendall(str(0 if code == 75 else code).encode())
            except BrokenPipeError:
                pass
            conn.close()


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == 'serve':
        serve()
    elif sys.argv[1:] in (['client'], ['checkout']):
        sys.exit(client(sys.argv[1] == 'checkout'))
    else:
        raise SystemExit('expected serve or client')
