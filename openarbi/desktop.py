"""Kitty integration. Runs using Kitty's bundled Python, never system Python."""
import fcntl
import json
import os
from pathlib import Path
import shlex
import socket
import stat
import subprocess
import sys
import tempfile
import time

from kittens.tui.handler import result_handler


def bundle():
    return Path(os.environ['OPENARBI_BUNDLE_DIR'])


def directory():
    if 'OPENARBI_DESKTOP_DIR' in os.environ:
        return Path(os.environ['OPENARBI_DESKTOP_DIR'])
    base = Path.home() / 'Library/Application Support' if sys.platform == 'darwin' else Path(os.environ.get('XDG_CONFIG_HOME', Path.home() / '.config'))
    return base / 'OpenArbi/desktop'


def broker(action, **fields):
    completed = subprocess.run([str(bundle() / 'bin/openarbi-desktop'), '--directory', str(directory()), 'command'],
                               input=json.dumps(dict(action=action, **fields)), text=True, capture_output=True, timeout=8)
    if completed.returncode:
        raise RuntimeError(completed.stderr.strip() or 'Workspace service unavailable')
    response = json.loads(completed.stdout)
    if response.get('error'):
        raise RuntimeError(response['error'])
    return response


def identity(window):
    while window.overlay_parent is not None:
        window = window.overlay_parent
    env = window.child.environ or window.child.final_env
    return env.get('OPENARBI_WINDOW_ID'), env.get('OPENARBI_TAB_ID')


def is_overlay(window):
    # Only a known top-level terminal may change the restoration layout.
    spec = window.creation_spec
    return spec is None or spec.overlay_for is not None


def launch_args(window_id, tab, device_id):
    args = ['--env', 'OPENARBI_WINDOW_ID=' + window_id, '--env', 'OPENARBI_TAB_ID=' + tab['id'],
            '--env', 'OPENARBI_DEVICE_ID=' + device_id]
    if tab['kind'] == 'local':
        args += [str(bundle() / 'share/openarbi/shell/launch.sh')]
    elif tab['kind'] == 'room':
        args += [str(bundle() / 'bin/kitty'), '+launch', str(bundle() / 'share/openarbi/desktop.py'), 'attach', tab['id']]
    else:
        raise ValueError('Unsupported tab kind')
    return args


def session_text(workspace, selected):
    lines = []
    for window in workspace['windows']:
        if window['id'] not in selected:
            continue
        if lines:
            lines.append('new_os_window')
        geometry = window['geometry']
        if geometry['width'] > 0 and geometry['height'] > 0:
            lines.append(f"os_window_size {geometry['width']} {geometry['height']}")
        for tab in window['tabs']:
            lines.append('new_tab ' + ('Shell' if tab['kind'] == 'local' else 'Workspace'))
            lines.append('launch ' + shlex.join(launch_args(window['id'], tab, workspace['device_id'])))
            if tab['id'] == window['selected_tab']:
                lines.append('focus')
    return '\n'.join(lines) + '\n'


def kitty_windows(path, device_id):
    if not path.exists():
        return []
    info = path.lstat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid():
        raise RuntimeError('The desktop socket belongs to an unidentified process')
    result = subprocess.run([str(bundle() / 'bin/kitty'), '@', '--to', 'unix:' + str(path), 'ls', '--all-env-vars'],
                            capture_output=True, text=True, timeout=5)
    if result.returncode:
        # Only a refused connection establishes that an owned socket is stale.
        with socket.socket(socket.AF_UNIX) as probe:
            probe.settimeout(1)
            try:
                probe.connect(str(path))
            except ConnectionRefusedError:
                path.unlink()
                return []
        raise RuntimeError('The desktop listener did not identify itself as OpenArbi')
    found = []
    for window in json.loads(result.stdout):
        for tab in window['tabs']:
            terminals = {t['id']: t for t in tab['windows']}
            grouped = [ident for group in tab['groups'] for ident in group['windows']]
            if len(grouped) != len(set(grouped)) or set(grouped) != set(terminals):
                raise RuntimeError('The desktop returned inconsistent terminal groups')
            for group in tab['groups']:
                if not group['windows']:
                    raise RuntimeError('The desktop returned an empty terminal group')
                # Kitty's first group member is the root terminal; following
                # members are its native overlays (picker, error, clipboard UI).
                terminal = terminals[group['windows'][0]]
                env = terminal['env']
                if env.get('OPENARBI_DEVICE_ID') != device_id or not env.get('OPENARBI_WINDOW_ID'):
                    raise RuntimeError('The desktop listener contains an unidentified window')
                if env['OPENARBI_WINDOW_ID'] not in found:
                    found.append(env['OPENARBI_WINDOW_ID'])
    return found


def gui_main():
    subprocess.run([str(bundle() / 'bin/openarbi-desktop'), '--directory', str(directory()), 'ensure'], check=True)
    # Serialize launch reconciliation through the actual GUI acknowledgement.
    # Concurrent launches cannot mistake a not-yet-visible window for a crash.
    lock_fd = os.open(directory() / 'gui.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        closing = directory() / 'closing.json'
        if closing.exists():
            pid = json.loads(closing.read_text())['pid']
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    closing.unlink()
                    break
                time.sleep(.05)
            else:
                raise RuntimeError('OpenArbi is still closing; retry after its windows finish closing')
        state = broker('status')['workspace']
        endpoint = directory() / 'kitty.sock'
        existing = kitty_windows(endpoint, state['device_id'])
        broker('sync-windows', order=existing)
        result = broker('launch')
        state = result['workspace']
        with tempfile.NamedTemporaryFile(mode='w', prefix='session-', dir=directory(), delete=False) as session:
            session.write(session_text(state, result['windows']))
        args = [str(bundle() / 'bin/kitty'), '--single-instance', '--instance-group', 'OpenArbi-' + state['device_id'],
                '--class', 'OpenArbi', '--name', 'OpenArbi', '--title', 'OpenArbi', '--listen-on', 'unix:' + str(endpoint),
                '--config', str(bundle() / 'share/openarbi/theme/kitty.conf'), '-o', 'allow_remote_control=socket-only',
                '-o', 'watcher=' + str(bundle() / 'share/openarbi/desktop.py'),
                '-o', 'confirm_os_window_close=0', '-o', 'macos_quit_when_last_window_closed=yes', '--session', session.name]
        action_file = shlex.quote(str(bundle() / 'share/openarbi/desktop.py'))
        mappings = {'ctrl+shift+t': 'new-tab', 'ctrl+shift+n': 'new-window', 'ctrl+shift+w': 'close-tab',
                    'ctrl+shift+r': 'reopen-tab', 'ctrl+shift+s': 'local-tab', 'ctrl+shift+p': 'picker',
                    'cmd+t': 'new-tab', 'cmd+n': 'new-window', 'cmd+w': 'close-tab', 'cmd+shift+t': 'reopen-tab'}
        for key, action in mappings.items():
            args += ['-o', f'map {key} kitten {action_file} {action}']
        for key, action in {'ctrl+shift+q': 'previous_tab', 'ctrl+shift+e': 'next_tab', 'cmd+shift+[': 'previous_tab', 'cmd+shift+]': 'next_tab'}.items():
            args += ['-o', f'map {key} {action}']
        for number in range(1, 10):
            for key in (f'ctrl+shift+{number}', f'cmd+{number}'):
                args += ['-o', f'map {key} goto_tab {number}']
        child = subprocess.Popen(args, start_new_session=True)
        try:
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                actual = kitty_windows(endpoint, state['device_id'])
                if set(result['windows']).issubset(actual):
                    return
                if child.poll() not in (None, 0):
                    raise RuntimeError('OpenArbi window failed to start')
                time.sleep(.05)
            raise RuntimeError('OpenArbi window did not become ready; your saved workspace is preserved')
        finally:
            Path(session.name).unlink(missing_ok=True)


def on_load(boss, data):
    if getattr(boss, '_openarbi_hooks', False):
        return
    boss._openarbi_hooks = True
    original_quit = boss.quit
    original_close = boss.close_tab_no_confirm
    original_window_closed = boss.on_os_window_closed
    boss._openarbi_quitting = False
    def persist(action, **fields):
        try:
            return broker(action, **fields)
        except Exception as error:
            # Native cleanup must still run during a broker outage. Keep an
            # explicit diagnostic for the next launch as well as Kitty's log.
            message = f'Workspace changes could not be saved: {error}'
            print(message, file=sys.stderr)
            try:
                (directory() / 'persistence-error.txt').write_text(message)
            except OSError as write_error:
                print(f'Could not save the diagnostic: {write_error}', file=sys.stderr)
    def quit_app(*args):
        boss._openarbi_quitting = True
        try:
            (directory() / 'closing.json').write_text(json.dumps({'pid': os.getpid()}))
            persist('quit')
        finally:
            original_quit(*args)
    def close_tab(tab):
        try:
            if tab.active_window:
                wid, tid = identity(tab.active_window)
                if wid and tid:
                    persist('close-tab', window_id=wid, tab_id=tid)
        finally:
            original_close(tab)
    def window_closed(os_id, *args):
        try:
            manager = boss.os_window_map.get(os_id)
            if len(boss.os_window_map) == 1:
                boss._openarbi_quitting = True
                (directory() / 'closing.json').write_text(json.dumps({'pid': os.getpid()}))
            if manager:
                for tab in manager:
                    if tab.active_window:
                        wid, _ = identity(tab.active_window)
                        if wid:
                            snapshot = persist('status')
                            if snapshot and any(w['id'] == wid for w in snapshot['workspace']['windows']):
                                persist('close-window', window_id=wid)
                            break
        finally:
            original_window_closed(os_id, *args)
    boss.quit = quit_app
    boss.close_tab_no_confirm = close_tab
    boss.on_os_window_closed = window_closed
    boss._openarbi_persist = persist
    # Native menus and inherited Kitty shortcuts must use the same allocation
    # path as the advertised shortcuts. No unmanaged shell windows are created.
    for name, action in (('new_tab', 'new-tab'), ('new_tab_with_cwd', 'new-tab'),
                         ('new_tab_with_wd', 'new-tab'), ('new_window', 'new-tab'),
                         ('new_window_with_cwd', 'new-tab'), ('new_os_window', 'new-window'),
                         ('new_os_window_with_cwd', 'new-window'), ('new_os_window_with_wd', 'new-window')):
        def create(*args, action=action):
            if boss.active_window:
                dispatch(boss, boss.active_window, action)
        setattr(boss, name, create)
    def detach(*args):
        boss.show_error('OpenArbi', 'Moving tabs between windows is not supported. Open the room in a new window using the room picker.')
    boss.detach_tab = detach
    boss.detach_window = detach


def on_close(boss, window, data):
    # ChildMonitor captured its Python callback before watcher initialization.
    # The close watcher runs from Window.destroy: child death has already removed
    # this window from the map, whereas whole OS-window destruction has not.
    if window.id in boss.window_id_map or boss._openarbi_quitting or is_overlay(window):
        return
    others = [w for w in boss.window_id_map.values() if not is_overlay(w)]
    if not others:
        return  # Last window closure preserves the workspace for restoration.
    wid, tid = identity(window)
    if wid and tid:
        snapshot = boss._openarbi_persist('status')
        if snapshot and any(w['id'] == wid and any(t['id'] == tid for t in w['tabs'])
                            for w in snapshot['workspace']['windows']):
            boss._openarbi_persist('close-tab', window_id=wid, tab_id=tid)


def on_focus_change(boss, window, data):
    if data.get('focused') and not is_overlay(window):
        # Global watcher loading happens during first Window construction, when
        # show_error has no active terminal. Report only after one gains focus.
        diagnostic = directory() / 'persistence-error.txt'
        if diagnostic.exists() and boss.active_window is not None:
            boss.show_error('OpenArbi workspace recovery', diagnostic.read_text())
            diagnostic.unlink()
        wid, tid = identity(window)
        if wid and tid:
            broker('select-tab', window_id=wid, tab_id=tid)


def on_resize(boss, window, data):
    from kitty.fast_data_types import get_os_window_pos, get_os_window_size
    from kitty.constants import is_wayland
    wid, _ = identity(window)
    size = get_os_window_size(window.os_window_id)
    if wid and size:
        x, y = (0, 0) if is_wayland() else get_os_window_pos(window.os_window_id)
        broker('geometry', window_id=wid, geometry=dict(x=x, y=y, width=size['width'], height=size['height']))


def on_tab_bar_dirty(boss, window, data):
    wid, _ = identity(window)
    manager = boss.os_window_map.get(window.os_window_id)
    if wid and manager:
        order = [identity(tab.active_window)[1] for tab in manager if tab.active_window]
        if order and all(order):
            snapshot = broker('status')['workspace']
            saved = next((w for w in snapshot['windows'] if w['id'] == wid), None)
            if saved and set(order) == {t['id'] for t in saved['tabs']}:
                broker('reorder-tabs', window_id=wid, order=order)


def main(args):
    return args[1]


def attach_loop(tab_id):
    command = [str(bundle() / 'bin/openarbi-desktop'), '--directory', str(directory()), 'attach', tab_id]
    while True:
        result = subprocess.run(command)
        message = 'Workspace disconnected.' if result.returncode == 0 else 'Workspace connection needs attention.'
        print('\n' + message)
        try:
            choice = input('Enter to reconnect · P for rooms · Q to close: ').strip().lower()
        except (EOFError, KeyboardInterrupt):
            return
        if choice == 'q':
            broker('close-tab', window_id=os.environ['OPENARBI_WINDOW_ID'], tab_id=tab_id)
            return
        if choice == 'p':
            subprocess.run([str(bundle() / 'bin/kitty'), '@', '--to', 'unix:' + str(directory() / 'kitty.sock'),
                            'action', 'kitten', str(bundle() / 'share/openarbi/picker.py')], check=True)
        elif choice:
            print('Choose Enter, P, or Q.')


@result_handler(no_ui=True)
def handle_result(args, data, target_window_id, boss):
    window = boss.window_id_map.get(target_window_id)
    if not window:
        return
    dispatch(boss, window, args[1])


def dispatch(boss, window, action, **fields):
    try:
        wid, _ = identity(window)
        if action == 'close-tab':
            boss.close_tab(window.tabref())
            return
        if action == 'picker':
            boss.kitten(str(bundle() / 'share/openarbi/picker.py'))
            return
        result = broker(action, window_id=wid, **fields)
        for new_window in result['workspace']['windows']:
            if action == 'new-window' and new_window['id'] not in result['windows']:
                continue
            if action != 'new-window' and new_window['id'] != wid:
                continue
            tab = next(t for t in new_window['tabs'] if t['id'] == result['tab_id'])
            boss.launch('--type=' + ('os-window' if action == 'new-window' else 'tab'),
                        '--tab-title', 'Shell' if tab['kind'] == 'local' else 'Workspace',
                        *launch_args(new_window['id'], tab, result['workspace']['device_id']))
    except Exception as error:
        boss.show_error('OpenArbi', str(error))


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == 'attach':
        attach_loop(sys.argv[2])
    elif len(sys.argv) == 1:
        gui_main()
    else:
        raise SystemExit('Unsupported desktop launch command')
