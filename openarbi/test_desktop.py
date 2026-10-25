"""Regression checks for the boundary between Kitty events and saved layout."""
import importlib.util
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


def load_desktop():
    handler = SimpleNamespace(result_handler=lambda **kw: lambda f: f)
    with patch.dict('sys.modules', {'kittens.tui.handler': handler}):
        spec = importlib.util.spec_from_file_location('openarbi_desktop_tested', Path(__file__).with_name('desktop.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module


def window(number, tab='tab', overlay=None):
    return SimpleNamespace(id=number, overlay_parent=None, creation_spec=SimpleNamespace(overlay_for=overlay), os_window_id=1,
                           child=SimpleNamespace(environ={'OPENARBI_WINDOW_ID': 'window', 'OPENARBI_TAB_ID': tab}))


class Hooks(unittest.TestCase):
    def test_mac_control_shortcuts_have_app_local_physical_aliases(self):
        module = load_desktop()
        with patch.object(module.sys, 'platform', 'darwin'):
            self.assertEqual(module.shortcut_keys('ctrl+shift+s'), ['ctrl+shift+s', 'ctrl+shift+0x1'])
            for key in ('q', 'w', 'e', 'r', 't', 'n', 'p', *'123456789'):
                self.assertEqual(len(module.shortcut_keys('ctrl+shift+' + key)), 2)
            for key in ('q', 'w', 'e', 'r', 'ctrl+c', 'ctrl+d', 'tab', 'cmd+t'):
                self.assertEqual(module.shortcut_keys(key), [key])
        with patch.object(module.sys, 'platform', 'linux'):
            self.assertEqual(module.shortcut_keys('ctrl+shift+s'), ['ctrl+shift+s'])

    def test_instance_group_preserves_identity_within_darwin_socket_limit(self):
        module = load_desktop()
        ident = '01234567-89ab-cdef-0123-456789abcdef'
        group = module.instance_group(ident)
        self.assertEqual(group, module.instance_group(ident))
        self.assertNotEqual(group, module.instance_group('11234567-89ab-cdef-0123-456789abcdef'))
        path = '/var/folders/dv/ngfnfzcx48d75l6gz1273f3m0000gn/C/kitty-ipc-501-' + group
        self.assertLess(len(path.encode()), 104)
        self.assertEqual(module.base64.urlsafe_b64decode(group[3:] + '=='), module.uuid.UUID(ident).bytes)

    def test_platform_window_flags_match_kitty_cli(self):
        module = load_desktop()
        with patch.object(module.sys, 'platform', 'darwin'):
            self.assertEqual(module.platform_window_args(), [])
        with patch.object(module.sys, 'platform', 'linux'):
            self.assertEqual(module.platform_window_args(), ['--class', 'OpenArbi', '--name', 'OpenArbi'])
        with patch.object(module.sys, 'platform', 'win32'):
            with self.assertRaises(RuntimeError):
                module.platform_window_args()

    def setUp(self):
        self.module = load_desktop()
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.module.directory = lambda: Path(self.temp.name)
        self.module.broker = Mock(return_value={'workspace': {'windows': []}})
        self.window = window(1)
        self.boss = SimpleNamespace(quit=Mock(), close_tab_no_confirm=Mock(), on_os_window_closed=Mock(),
                                    on_child_death=Mock(), show_error=Mock(), os_window_map={},
                                    window_id_map={1: self.window}, active_window=self.window)
        self.quit = self.boss.quit
        self.close = self.boss.close_tab_no_confirm
        self.death = self.boss.on_child_death
        self.os_close = self.boss.on_os_window_closed
        self.module.on_load(self.boss, {})

    def test_broker_failure_does_not_prevent_native_quit_or_close(self):
        self.module.broker.side_effect = RuntimeError('broker disconnected')
        self.boss.quit()
        self.quit.assert_called_once()
        self.boss.close_tab_no_confirm(SimpleNamespace(active_window=self.window))
        self.close.assert_called_once()
        self.boss.on_os_window_closed(1)
        self.os_close.assert_called_once_with(1)
        self.assertIn('broker disconnected', (Path(self.temp.name) / 'persistence-error.txt').read_text())

    def test_native_creation_actions_use_managed_allocation(self):
        self.module.dispatch = Mock()
        for name, action in [('new_window', 'new-tab'), ('new_tab', 'new-tab'), ('new_os_window', 'new-window')]:
            getattr(self.boss, name)()
            self.module.dispatch.assert_called_with(self.boss, self.window, action)

    def test_recovery_diagnostic_waits_for_real_terminal_focus(self):
        diagnostic = Path(self.temp.name) / 'persistence-error.txt'
        diagnostic.write_text('Previous layout change was not saved')
        self.boss.active_window = None
        self.module.on_focus_change(self.boss, self.window, {'focused': True})
        self.assertTrue(diagnostic.exists())
        self.boss.show_error.assert_not_called()
        self.boss.active_window = self.window
        self.module.on_focus_change(self.boss, self.window, {'focused': True})
        self.boss.show_error.assert_called_once()
        self.assertFalse(diagnostic.exists())

    def test_detach_cannot_duplicate_saved_window_identity(self):
        self.boss.detach_tab()
        self.boss.detach_window()
        self.assertEqual(self.boss.show_error.call_count, 2)
        self.module.broker.assert_not_called()

    def test_actions_on_overlay_use_underlying_room_identity(self):
        overlay = window(2, 'unmanaged', overlay=1)
        overlay.overlay_parent = self.window
        self.assertEqual(self.module.identity(overlay), ('window', 'tab'))
        self.boss.close_tab_no_confirm(SimpleNamespace(active_window=overlay))
        self.module.broker.assert_called_once_with('close-tab', window_id='window', tab_id='tab')

    def test_nonlast_shell_exit_removes_saved_tab(self):
        self.boss.window_id_map[2] = window(2, 'other')
        self.boss.window_id_map.pop(1)
        self.module.broker.return_value = {'workspace': {'windows': [{'id': 'window', 'tabs': [{'id': 'tab'}, {'id': 'other'}]}]}}
        self.module.on_close(self.boss, self.window, {})
        self.module.broker.assert_called_with('close-tab', window_id='window', tab_id='tab')

    def test_explicit_close_followed_by_child_exit_is_not_repeated(self):
        self.boss.window_id_map[2] = window(2, 'other')
        self.boss.close_tab_no_confirm(SimpleNamespace(active_window=self.window))
        self.module.broker.assert_called_once_with('close-tab', window_id='window', tab_id='tab')
        self.module.broker.reset_mock()
        self.module.broker.return_value = {'workspace': {'windows': [{'id': 'window', 'tabs': [{'id': 'other'}]}]}}
        self.boss.window_id_map.pop(1)
        self.module.on_close(self.boss, self.window, {})
        self.module.broker.assert_called_once_with('status')

    def test_last_terminal_exit_preserves_workspace(self):
        self.boss.window_id_map.pop(1)
        self.module.on_close(self.boss, self.window, {})
        self.module.broker.assert_not_called()

    def test_quit_and_overlay_death_preserve_saved_tabs(self):
        self.boss.window_id_map[2] = window(2, overlay=1)
        self.module.on_close(self.boss, self.boss.window_id_map.pop(2), {})
        self.module.broker.assert_not_called()
        self.boss.quit()
        self.module.broker.reset_mock()
        self.module.on_close(self.boss, self.boss.window_id_map.pop(1), {})
        self.module.broker.assert_not_called()

    def test_tab_reorder_uses_actual_kitty_order(self):
        self.boss.os_window_map[1] = [SimpleNamespace(active_window=window(2, 'second')), SimpleNamespace(active_window=self.window)]
        self.module.broker.return_value = {'workspace': {'windows': [{'id': 'window', 'tabs': [{'id': 'tab'}, {'id': 'second'}]}]}}
        self.module.on_tab_bar_dirty(self.boss, self.window, {})
        self.module.broker.assert_called_with('reorder-tabs', window_id='window', order=['second', 'tab'])


if __name__ == '__main__':
    unittest.main()
