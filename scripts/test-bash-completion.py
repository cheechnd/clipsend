#!/usr/bin/env python3
"""Exercise generated Bash completion through real readline in a disposable PTY.

Invoked by TestGeneratedBashReadline; never sources the user's shell configuration.
"""
import errno
import os
from pathlib import Path
import pty
import select
import shlex
import subprocess
import sys
import time

script, directory = sys.argv[1:]
Path(directory, "quoted image.png").touch()
Path(directory, "other file.pdf").touch()
master, slave = pty.openpty()
env = dict(os.environ, TERM="dumb", PS1="READY> ", PROMPT_COMMAND="", INPUTRC="/dev/null")
process = subprocess.Popen(["bash", "--noprofile", "--norc", "-i"], cwd=directory,
                           env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
os.close(slave)

def read_until(marker):
    data = b""
    deadline = time.monotonic() + 15
    while marker not in data:
        if time.monotonic() > deadline:
            raise AssertionError(f"readline timeout: {data!r}")
        ready, _, _ = select.select([master], [], [], .1)
        if ready:
            try:
                chunk = os.read(master, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    raise AssertionError(f"bash exited: {data!r}") from error
                raise
            if not chunk:
                raise AssertionError(f"bash closed: {data!r}")
            data += chunk
    return data

try:
    read_until(b"READY> ")
    setup = ("source /usr/share/bash-completion/bash_completion; source " + shlex.quote(script)
             + "; bind 'set bell-style none'; "
             + "bind -x '\"\\C-g\":printf \"\\nRESULT:%s\\nENDRESULT\\n\" \"$READLINE_LINE\"'\n")
    os.write(master, setup.encode())
    read_until(b"READY> ")
    cases = [
        ("clipsend ipa", ["clipsend", "ipad"]),
        ("clipsend send IPa", ["clipsend", "send", "IPad"]),
        ("clipsend work", ["clipsend", "work Phone"]),
        ("clipsend rename ipad new", ["clipsend", "rename", "ipad", "new"]),
        ('clipsend ipad "quo', ["clipsend", "ipad", "quoted image.png"]),
        ('clipsend send ipad "quoted image.png" "oth', ["clipsend", "send", "ipad", "quoted image.png", "other file.pdf"]),
        ('clipsend pair fresh --qr "quo', ["clipsend", "pair", "fresh", "--qr", "quoted image.png"]),
        ("clipsend ipad --st", ["clipsend", "ipad", "--stdin"]),
    ]
    for line, expected in cases:
        os.write(master, line.encode() + b"\t\x07")
        output = read_until(b"\r\nENDRESULT\r\n")
        actual_line = output.split(b"\r\nRESULT:")[-1].split(b"\r\nENDRESULT")[0].decode()
        actual = shlex.split(actual_line)
        if actual != expected:
            raise AssertionError(f"{line!r}: {actual!r} != {expected!r}; output={output!r}")
        os.write(master, b"\x01\x0b")  # Clear the input without executing it.
    print("Generated Bash readline: aliases, case, rename position, quoted paths, flags passed")
finally:
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
    os.close(master)
