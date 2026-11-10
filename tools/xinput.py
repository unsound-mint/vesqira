#!/usr/bin/env python3
"""Minimal X11 input for the private dev screen (no xdotool needed): click X Y | key NAME... | type TEXT.
Uses libX11 and libXtst through ctypes. Key names are X keysym names (Return, Escape, Tab, ctrl+l)."""
import ctypes, ctypes.util, os, sys, time

x11 = ctypes.CDLL(ctypes.util.find_library("X11") or "libX11.so.6")
xtst = ctypes.CDLL(ctypes.util.find_library("Xtst") or "libXtst.so.6")
x11.XOpenDisplay.restype = ctypes.c_void_p
x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
x11.XStringToKeysym.restype = ctypes.c_ulong
x11.XKeysymToKeycode.restype = ctypes.c_ubyte
x11.XKeysymToKeycode.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
x11.XFlush.argtypes = [ctypes.c_void_p]
x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
xtst.XTestFakeKeyEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
xtst.XTestFakeButtonEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
xtst.XTestFakeMotionEvent.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_ulong]

d = x11.XOpenDisplay((os.environ.get("DISPLAY") or ":99").encode())
if not d:
    sys.exit("cannot open the dev display (run tools/dev-desktop.sh up first)")

def keycode(name):
    ks = x11.XStringToKeysym(name.encode())
    if not ks:
        sys.exit(f"unknown key {name!r}")
    return x11.XKeysymToKeycode(d, ks)

def press(names):
    codes = [keycode(n) for n in names]
    for c in codes: xtst.XTestFakeKeyEvent(d, c, 1, 0)
    for c in reversed(codes): xtst.XTestFakeKeyEvent(d, c, 0, 0)
    x11.XFlush(d); time.sleep(0.03)

PLAIN = {" ": "space", "\n": "Return", "/": "slash", ".": "period", "-": "minus", ",": "comma", "=": "equal", ";": "semicolon", "'": "apostrophe", "`": "grave", "[": "bracketleft", "]": "bracketright", "\\": "backslash"}
SHIFTED = {"_": "minus", "@": "2", ":": "semicolon", "~": "grave", "!": "1", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0", "+": "equal", '"': "apostrophe", "<": "comma", ">": "period", "?": "slash", "{": "bracketleft", "}": "bracketright", "|": "backslash"}
def type_text(s):
    for ch in s:
        if ch in PLAIN: press([PLAIN[ch]])
        elif ch in SHIFTED: press(["Shift_L", SHIFTED[ch]])
        elif ch.isupper(): press(["Shift_L", ch.lower()])
        else: press([ch])

cmd, args = sys.argv[1], sys.argv[2:]
if cmd == "click":
    xtst.XTestFakeMotionEvent(d, 0, int(args[0]), int(args[1]), 0); x11.XFlush(d); time.sleep(0.1)
    xtst.XTestFakeButtonEvent(d, 1, 1, 0); xtst.XTestFakeButtonEvent(d, 1, 0, 0); x11.XFlush(d)
elif cmd == "key":
    for a in args: press(a.replace("ctrl", "Control_L").replace("shift", "Shift_L").replace("alt", "Alt_L").split("+"))
elif cmd == "type":
    type_text(" ".join(args))
else:
    sys.exit(__doc__)
x11.XCloseDisplay(d)
