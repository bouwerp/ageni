package tools

// Simulator provides iOS Simulator (via xcrun simctl / idb) and Android
// Emulator (via adb) control to sub-agents. It enables the full UI-automation
// loop: launch app → screenshot → tap/swipe/type → screenshot → verify.
//
// iOS requires macOS with Xcode installed. Optional: Facebook's idb
// (https://fbidb.io) for richer UI interactions; the tool degrades gracefully
// to simctl-only when idb is absent.
//
// Android requires adb (Android SDK platform-tools) on PATH. The emulator
// must already be running (or use action=boot with the AVD name).

import (
"bytes"
"context"
"encoding/json"
"errors"
"fmt"
"os"
"os/exec"
"path/filepath"
"runtime"
"strings"
"time"
)

// simArgs holds the parsed arguments for a simulator tool call.
type simArgs struct {
Platform    string          `json:"platform"`
Action      string          `json:"action"`
Device      string          `json:"device"`
App         string          `json:"app"`
URL         string          `json:"url"`
X           float64         `json:"x"`
Y           float64         `json:"y"`
X2          float64         `json:"x2"`
Y2          float64         `json:"y2"`
DurationMs  int             `json:"duration_ms"`
Text        string          `json:"text"`
OutputPath  string          `json:"output_path"`
PushPayload json.RawMessage `json:"push_payload"`
}

// Simulator implements mobile simulator / emulator control.
type Simulator struct{}

func (Simulator) Name() string { return "simulator" }

func (Simulator) Description() string {
return `Control iOS Simulator (xcrun simctl) and Android Emulator (adb).
Supports booting devices, installing/launching apps, taking screenshots,
and sending UI interactions (tap, swipe, type). Designed for the full
visual-feedback loop: screenshot → inspect with view_image → tap/type → repeat.

iOS requires macOS with Xcode installed.
Android requires adb (Android SDK platform-tools) on PATH.

Actions:
  list_devices      — list available simulators / connected ADB devices
  boot              — boot a simulator (iOS) or start an AVD (Android)
  shutdown          — shut down a simulator / stop emulator
  install           — install an app bundle (.app for iOS, .apk for Android)
  launch            — launch an installed app by bundle ID (iOS) or pkg/Activity (Android)
  terminate         — stop a running app
  screenshot        — capture the screen; returns the saved file path (pass to view_image)
  tap               — tap at screen coordinates (x, y)
  swipe             — swipe from (x1,y1) to (x2,y2)
  type_text         — type text into the focused field
  key_event         — send a hardware key (Android: KEYCODE_* names)
  open_url          — open a URL / custom scheme in the simulator
  push_notification — send a simulated APNs push notification (iOS only)
  shell             — run an arbitrary adb shell command (Android only)`
}

func (Simulator) Schema() json.RawMessage {
return json.RawMessage(`{
  "type": "object",
  "properties": {
    "platform": {
      "type": "string",
      "enum": ["ios", "android"],
      "description": "Target platform."
    },
    "action": {
      "type": "string",
      "enum": [
        "list_devices", "boot", "shutdown",
        "install", "launch", "terminate",
        "screenshot", "tap", "swipe", "type_text", "key_event",
        "open_url", "push_notification", "shell"
      ],
      "description": "Operation to perform."
    },
    "device": {
      "type": "string",
      "description": "Device identifier. iOS: simulator UDID or name (e.g. 'iPhone 16 Pro') — defaults to 'booted'. Android: adb serial from 'adb devices' — defaults to first connected device."
    },
    "app": {
      "type": "string",
      "description": "For install: path to .app (iOS) or .apk (Android). For launch/terminate: bundle ID (iOS) or 'pkg/ActivityName' (Android)."
    },
    "url": {"type": "string", "description": "URL or custom scheme for open_url."},
    "x":  {"type": "number", "description": "X coordinate for tap/swipe (logical pixels)."},
    "y":  {"type": "number", "description": "Y coordinate for tap/swipe (logical pixels)."},
    "x2": {"type": "number", "description": "End X coordinate for swipe."},
    "y2": {"type": "number", "description": "End Y coordinate for swipe."},
    "duration_ms": {"type": "integer", "description": "Swipe duration in ms (default 300)."},
    "text": {
      "type": "string",
      "description": "Text to type (type_text), KEYCODE name (key_event, e.g. 'BACK'), or shell command (shell)."
    },
    "output_path": {
      "type": "string",
      "description": "Where to save the screenshot. Defaults to a temp file. The path is returned in the response."
    },
    "push_payload": {
      "type": "object",
      "description": "APNs payload for push_notification (iOS only), e.g. {\"aps\":{\"alert\":\"Hello\"}}. Requires 'app' (bundle ID)."
    }
  },
  "required": ["platform", "action"]
}`)
}

func (s Simulator) Call(ctx context.Context, rawArgs json.RawMessage) (string, error) {
var a simArgs
if err := json.Unmarshal(rawArgs, &a); err != nil {
return "", fmt.Errorf("invalid args: %w", err)
}
if a.Platform == "" {
return "", errors.New("platform is required (ios or android)")
}
if a.Action == "" {
return "", errors.New("action is required")
}
switch a.Platform {
case "ios":
return s.ios(ctx, a)
case "android":
return s.android(ctx, a)
default:
return "", fmt.Errorf("unsupported platform %q; must be 'ios' or 'android'", a.Platform)
}
}

// ── iOS (xcrun simctl + optional idb) ────────────────────────────────────────

func (s Simulator) ios(ctx context.Context, a simArgs) (string, error) {
if runtime.GOOS != "darwin" {
return "", errors.New("iOS Simulator control requires macOS")
}
if _, err := exec.LookPath("xcrun"); err != nil {
return "", errors.New("xcrun not found — install Xcode from the App Store")
}

device := a.Device
if device == "" {
device = "booted"
}

simctl := func(args ...string) (string, error) {
return simRun(ctx, "xcrun", append([]string{"simctl"}, args...)...)
}

switch a.Action {
case "list_devices":
return simctl("list", "devices", "--json")

case "boot":
if device == "booted" {
return "", errors.New("device UDID or name required for boot")
}
out, err := simctl("boot", device)
if err != nil && strings.Contains(out, "Unable to boot device in current state: Booted") {
return "already booted", nil
}
if err != nil {
return out, err
}
return "booted: " + device, nil

case "shutdown":
return simctl("shutdown", device)

case "install":
if a.App == "" {
return "", errors.New("app (.app bundle path) is required for install")
}
return simctl("install", device, a.App)

case "launch":
if a.App == "" {
return "", errors.New("app (bundle ID) is required for launch")
}
return simctl("launch", device, a.App)

case "terminate":
if a.App == "" {
return "", errors.New("app (bundle ID) is required for terminate")
}
return simctl("terminate", device, a.App)

case "screenshot":
path := a.OutputPath
if path == "" {
path = filepath.Join(os.TempDir(), fmt.Sprintf("ageni_ios_%d.png", time.Now().UnixMilli()))
}
if _, err := simctl("io", device, "screenshot", path); err != nil {
return "", err
}
return fmt.Sprintf("screenshot saved: %s\nPass this path to view_image to inspect the screen.", path), nil

case "tap":
// Prefer idb (Facebook's iOS Development Bridge) for UI gestures.
if out, err := idbUI(ctx, device, "tap", fmt.Sprintf("%.0f", a.X), fmt.Sprintf("%.0f", a.Y)); err == nil {
return out, nil
}
// Fall back to simctl io sendEvent (requires Xcode 14.3+).
return simctl("io", device, "sendEvent",
fmt.Sprintf(`{"type":"mouseDown","x":%.0f,"y":%.0f}`, a.X, a.Y),
fmt.Sprintf(`{"type":"mouseUp","x":%.0f,"y":%.0f}`, a.X, a.Y),
)

case "swipe":
dur := a.DurationMs
if dur <= 0 {
dur = 300
}
if out, err := idbUI(ctx, device, "swipe",
fmt.Sprintf("%.0f", a.X), fmt.Sprintf("%.0f", a.Y),
fmt.Sprintf("%.0f", a.X2), fmt.Sprintf("%.0f", a.Y2),
"--duration", fmt.Sprintf("%.2f", float64(dur)/1000),
); err == nil {
return out, nil
}
// AppleScript fallback.
script := fmt.Sprintf(
`tell application "Simulator" to activate\ntell application "System Events"\n  tell process "Simulator"\n    drag from {%.0f, %.0f} to {%.0f, %.0f}\n  end tell\nend tell`,
a.X, a.Y, a.X2, a.Y2,
)
return simRun(ctx, "osascript", "-e", script)

case "type_text":
if a.Text == "" {
return "", errors.New("text is required for type_text")
}
if out, err := idbUI(ctx, device, "text", a.Text); err == nil {
return out, nil
}
script := fmt.Sprintf(
`tell application "Simulator" to activate\ntell application "System Events"\n  keystroke %q\nend tell`,
a.Text,
)
return simRun(ctx, "osascript", "-e", script)

case "key_event":
return "", errors.New("key_event is Android-only; use swipe for back-navigation or type_text for keyboard input on iOS")

case "open_url":
if a.URL == "" {
return "", errors.New("url is required for open_url")
}
return simctl("openurl", device, a.URL)

case "push_notification":
if a.App == "" {
return "", errors.New("app (bundle ID) is required for push_notification")
}
if len(a.PushPayload) == 0 {
return "", errors.New("push_payload is required for push_notification")
}
tmp, err := os.CreateTemp("", "ageni_apns_*.json")
if err != nil {
return "", fmt.Errorf("create temp file: %w", err)
}
defer os.Remove(tmp.Name())
if _, err := tmp.Write(a.PushPayload); err != nil {
return "", fmt.Errorf("write payload: %w", err)
}
tmp.Close()
return simctl("push", device, a.App, tmp.Name())

case "shell":
return "", errors.New("shell is Android-only; use run_bash for iOS host-side commands")

default:
return "", fmt.Errorf("unknown action: %s", a.Action)
}
}

// idbUI invokes idb ui <sub-args> if idb is on PATH. Returns an error if idb
// is not installed or the command fails, so callers can fall through to simctl.
func idbUI(ctx context.Context, udid string, args ...string) (string, error) {
idbPath, err := exec.LookPath("idb")
if err != nil {
return "", errors.New("idb not installed")
}
all := append([]string{"--log", "ERROR", "ui"}, args...)
if udid != "" && udid != "booted" {
all = append(all, "--udid", udid)
}
return simRun(ctx, idbPath, all...)
}

// ── Android (adb) ────────────────────────────────────────────────────────────

func (s Simulator) android(ctx context.Context, a simArgs) (string, error) {
adbPath, err := exec.LookPath("adb")
if err != nil {
return "", errors.New("adb not found — install Android SDK platform-tools and add to PATH")
}

adb := func(args ...string) (string, error) {
all := args
if a.Device != "" {
all = append([]string{"-s", a.Device}, args...)
}
return simRun(ctx, adbPath, all...)
}

switch a.Action {
case "list_devices":
return simRun(ctx, adbPath, "devices", "-l")

case "boot":
emulatorPath, err := exec.LookPath("emulator")
if err != nil {
return "", errors.New("emulator binary not found — install Android Emulator via Android Studio SDK Manager")
}
avd := a.Device
if avd == "" {
return "", errors.New("device (AVD name) is required to boot an Android emulator")
}
cmd := exec.CommandContext(ctx, emulatorPath, "-avd", avd)
if err := cmd.Start(); err != nil {
return "", fmt.Errorf("start emulator: %w", err)
}
return fmt.Sprintf("emulator '%s' starting (PID %d) — use list_devices after ~10s to confirm it is ready", avd, cmd.Process.Pid), nil

case "shutdown":
return adb("emu", "kill")

case "install":
if a.App == "" {
return "", errors.New("app (.apk path) is required for install")
}
return adb("install", "-r", a.App)

case "launch":
if a.App == "" {
return "", errors.New("app ('pkg/ActivityName' or 'pkg') is required for launch")
}
if !strings.Contains(a.App, "/") {
return adb("shell", "monkey", "-p", a.App, "-c", "android.intent.category.LAUNCHER", "1")
}
parts := strings.SplitN(a.App, "/", 2)
return adb("shell", "am", "start", "-n", parts[0]+"/"+parts[1])

case "terminate":
if a.App == "" {
return "", errors.New("app (package name) is required for terminate")
}
return adb("shell", "am", "force-stop", a.App)

case "screenshot":
path := a.OutputPath
if path == "" {
path = filepath.Join(os.TempDir(), fmt.Sprintf("ageni_adb_%d.png", time.Now().UnixMilli()))
}
cmdArgs := []string{"exec-out", "screencap", "-p"}
if a.Device != "" {
cmdArgs = append([]string{"-s", a.Device}, cmdArgs...)
}
cmd := exec.CommandContext(ctx, adbPath, cmdArgs...)
data, err := cmd.Output()
if err != nil {
return "", fmt.Errorf("screencap: %w", err)
}
if err := os.WriteFile(path, data, 0o644); err != nil {
return "", fmt.Errorf("save screenshot: %w", err)
}
return fmt.Sprintf("screenshot saved: %s\nPass this path to view_image to inspect the screen.", path), nil

case "tap":
return adb("shell", "input", "tap",
fmt.Sprintf("%.0f", a.X), fmt.Sprintf("%.0f", a.Y))

case "swipe":
dur := a.DurationMs
if dur <= 0 {
dur = 300
}
return adb("shell", "input", "swipe",
fmt.Sprintf("%.0f", a.X), fmt.Sprintf("%.0f", a.Y),
fmt.Sprintf("%.0f", a.X2), fmt.Sprintf("%.0f", a.Y2),
fmt.Sprintf("%d", dur))

case "type_text":
if a.Text == "" {
return "", errors.New("text is required for type_text")
}
// adb input text doesn't handle spaces or special chars without escaping.
escaped := strings.ReplaceAll(a.Text, `\`, `\\`)
escaped = strings.ReplaceAll(escaped, " ", "%s")
escaped = strings.ReplaceAll(escaped, "'", `\'`)
return adb("shell", "input", "text", escaped)

case "key_event":
if a.Text == "" {
return "", errors.New("text (KEYCODE name, e.g. 'BACK' or 'KEYCODE_BACK') is required for key_event")
}
key := strings.ToUpper(a.Text)
if !strings.HasPrefix(key, "KEYCODE_") {
key = "KEYCODE_" + key
}
return adb("shell", "input", "keyevent", key)

case "open_url":
if a.URL == "" {
return "", errors.New("url is required for open_url")
}
return adb("shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", a.URL)

case "push_notification":
return "", errors.New("push_notification is iOS-only; use FCM test tools or adb shell am broadcast for Android")

case "shell":
if a.Text == "" {
return "", errors.New("text (shell command) is required for shell action")
}
return adb("shell", a.Text)

default:
return "", fmt.Errorf("unknown action: %s", a.Action)
}
}

// simRun executes a command with a 120-second timeout and returns combined
// stdout+stderr. Non-zero exit is returned as an error with the output embedded.
func simRun(ctx context.Context, name string, args ...string) (string, error) {
tctx, cancel := context.WithTimeout(ctx, 120*time.Second)
defer cancel()
cmd := exec.CommandContext(tctx, name, args...)
var buf bytes.Buffer
cmd.Stdout = &buf
cmd.Stderr = &buf
err := cmd.Run()
out := strings.TrimSpace(buf.String())
if err != nil {
var ee *exec.ExitError
if errors.As(err, &ee) {
return out, fmt.Errorf("exit %d: %s", ee.ExitCode(), out)
}
if tctx.Err() == context.DeadlineExceeded {
return out, errors.New("command timed out after 120s")
}
return out, err
}
return out, nil
}
