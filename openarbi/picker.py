"""Authenticated room selection and explicit recovery, using bundled Python."""
import os
from pathlib import Path
import sys
import unicodedata

sys.path.insert(0, str(Path(os.environ['OPENARBI_BUNDLE_DIR']) / 'share/openarbi'))
from desktop import broker, dispatch
from kittens.tui.handler import result_handler


def text(value):
    return ''.join(c for c in str(value) if c.isprintable() and not unicodedata.category(c).startswith('C'))


def main(args):
    while True:
        try:
            rooms = broker('rooms').get('rooms', [])
            print('\033[2J\033[HOpenArbi · Your workspaces\n')
            for index, room in enumerate(rooms, 1):
                print(f"{index:2d}  {text(room.get('name') or room['id'])}  ·  {text(room['state'])}")
            if not rooms:
                print('No saved rooms yet.')
            print('\nNumber: open in a new tab · S number: stop · R number: recover · Q: close')
            choice = input('> ').strip().lower().split()
            if choice == ['q']:
                return None
            if len(choice) == 1 and choice[0].isdigit():
                number, action = int(choice[0]), 'open'
            elif len(choice) == 2 and choice[0] in ('s', 'r') and choice[1].isdigit():
                action, number = choice[0], int(choice[1])
            else:
                print('Choose a room number or the listed action.'); input('Enter to continue'); continue
            if number < 1 or number > len(rooms):
                print('That room number is unavailable.'); input('Enter to continue'); continue
            room_id = rooms[number - 1]['id']
            if action == 'open':
                return {'room_id': room_id}
            verb = 'stop' if action == 's' else 'resume'
            if input(f'{verb.capitalize()} this workspace? [y/N] ').strip().lower() == 'y':
                broker(verb + '-room', room_id=room_id)
        except (EOFError, KeyboardInterrupt):
            return None
        except Exception as error:
            print(text(error))
            input('Enter to retry')


@result_handler()
def handle_result(args, data, target_window_id, boss):
    window = boss.window_id_map.get(target_window_id)
    if window and data:
        dispatch(boss, window, 'open-room', room_id=data['room_id'])
