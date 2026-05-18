---
name: android-emulator
description: Android emulator and device automation via ADB, AVD Manager, sdkmanager, and platform-tools — launching/managing emulators, installing APKs, logcat, UI testing with UIAutomator/Espresso, screen capture, and CI integration. Triggers "android emulator", "adb", "avd", "android sdk", "android simulator", "apk install", "logcat", "uiautomator", "espresso", "android test", "adb shell".
version: 1.0.0
---
# Android Emulator & ADB Skill

Structured workflows for managing Android Virtual Devices (AVDs), interacting with emulators and physical devices via ADB, using SDK platform-tools, and automating Android testing.

## Core Principles

1. **Verify device state first.** Always run `adb devices` before issuing commands — stale or offline devices cause confusing failures.
2. **Wait for boot completion.** Emulators take 30–120 s to boot; poll `sys.boot_completed` rather than sleeping blindly.
3. **Use `-s <serial>` for multi-device clarity.** When more than one device/emulator is connected, always specify the target.
4. **Prefer `adb shell` over raw socket tricks.** `adb shell` is stable across API levels; direct socket access is fragile.
5. **Clean up after tests.** Uninstall test APKs, clear data, and kill emulators that are no longer needed.
6. **Never hard-code API levels.** Read `adb shell getprop ro.build.version.sdk` and branch on it.

## Environment Prerequisites

```bash
# Verify SDK tools are on PATH
which adb emulator sdkmanager avdmanager

# Typical SDK layout (set ANDROID_HOME / ANDROID_SDK_ROOT)
export ANDROID_HOME=$HOME/Library/Android/sdk          # macOS
export ANDROID_HOME=$HOME/Android/Sdk                  # Linux
export PATH=$PATH:$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$ANDROID_HOME/cmdline-tools/latest/bin

# Confirm versions
adb --version
emulator -version | head -1
sdkmanager --version
```

## SDK Management (sdkmanager)

```bash
# List installed packages
sdkmanager --list_installed

# List available packages (pipe to grep to filter)
sdkmanager --list | grep "system-images;android-35"

# Install a system image (Google APIs variant is required for Google Play services)
sdkmanager "system-images;android-35;google_apis;x86_64"
sdkmanager "system-images;android-34;google_apis_playstore;x86_64"

# Install platform-tools and build-tools
sdkmanager "platform-tools" "build-tools;35.0.0" "platforms;android-35"

# Accept all licences non-interactively
yes | sdkmanager --licenses

# Update everything
sdkmanager --update
```

## AVD Management (avdmanager / emulator)

```bash
# List existing AVDs
avdmanager list avd
emulator -list-avds

# Create an AVD
avdmanager create avd \
  --name "Pixel_9_API_35" \
  --package "system-images;android-35;google_apis;x86_64" \
  --device "pixel_9" \
  --force

# Delete an AVD
avdmanager delete avd --name "Pixel_9_API_35"

# Start an emulator (background; -no-window for CI)
emulator -avd Pixel_9_API_35 -no-snapshot-load &

# Start headless (CI / no GPU)
emulator -avd Pixel_9_API_35 -no-window -no-audio -no-boot-anim -gpu swiftshader_indirect &

# Kill a running emulator gracefully
adb -s emulator-5554 emu kill
```

## Waiting for Boot

```bash
# Poll until fully booted (use this instead of sleep)
wait_for_boot() {
  local serial="${1:-}"
  local flag="-s $serial"
  [[ -z "$serial" ]] && flag=""
  echo "Waiting for emulator to boot..."
  adb $flag wait-for-device
  until [[ "$(adb $flag shell getprop sys.boot_completed 2>/dev/null)" == "1" ]]; do
    sleep 2
  done
  echo "Emulator ready."
}
wait_for_boot emulator-5554
```

## ADB — Device & Connection

```bash
# List connected devices and emulators (with state)
adb devices -l

# Connect to a TCP/IP device (e.g. physical device on LAN or Genymotion)
adb connect 192.168.1.42:5555
adb disconnect 192.168.1.42:5555

# Restart ADB server (fixes "offline" devices)
adb kill-server && adb start-server

# Identify emulator serial from AVD name (useful in CI)
adb devices | grep emulator | awk '{print $1}'
```

## APK Install / Uninstall

```bash
# Install (use -r to reinstall, -t to allow test APKs, -d to allow downgrade)
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb install -r -t app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk

# Install to a specific device
adb -s emulator-5554 install -r my.apk

# Uninstall (keeps data unless you use shell pm clear)
adb uninstall com.example.myapp

# Clear app data without uninstalling
adb shell pm clear com.example.myapp

# List installed packages
adb shell pm list packages | grep example
```

## Launching Activities & Intents

```bash
# Launch the default activity of an app
adb shell monkey -p com.example.myapp -c android.intent.category.LAUNCHER 1

# Start a specific activity
adb shell am start -n com.example.myapp/.MainActivity

# Start with extras
adb shell am start \
  -n com.example.myapp/.DeepLinkActivity \
  -a android.intent.action.VIEW \
  -d "myapp://screen/home"

# Force-stop an app
adb shell am force-stop com.example.myapp

# Broadcast an intent
adb shell am broadcast -a com.example.MY_ACTION --es key value
```

## Logcat

```bash
# Stream all logs
adb logcat

# Filter by tag and level (V/D/I/W/E/F)
adb logcat -s MyTag:D AndroidRuntime:E

# Filter by package (API 24+)
adb logcat --pid=$(adb shell pidof -s com.example.myapp)

# Capture to file with timestamp
adb logcat -v threadtime > logcat_$(date +%s).txt &

# Clear logcat buffer before a test run
adb logcat -c

# Pretty-print with colour (requires Python/pidcat)
# https://github.com/JakeWharton/pidcat
pidcat com.example.myapp
```

## Screen Capture & Recording

```bash
# Screenshot → pull to host
adb shell screencap -p /sdcard/screen.png
adb pull /sdcard/screen.png ./screen.png
adb shell rm /sdcard/screen.png

# Screen record (up to 3 min; Ctrl-C stops)
adb shell screenrecord /sdcard/demo.mp4
adb pull /sdcard/demo.mp4 ./demo.mp4

# One-liner: record and pull
adb shell screenrecord /sdcard/r.mp4 & sleep 10 && kill %1 && adb pull /sdcard/r.mp4
```

## File Transfer

```bash
# Push file to device
adb push ./local_file.txt /sdcard/Download/file.txt

# Pull file from device
adb pull /sdcard/Download/file.txt ./local_file.txt

# Pull entire directory
adb pull /sdcard/DCIM ./dcim_backup

# Run as app UID (root not required, scoped storage aware)
adb shell run-as com.example.myapp cat /data/data/com.example.myapp/databases/app.db > app.db
```

## Shell Commands & System Props

```bash
# Drop into interactive shell
adb shell

# Read system properties
adb shell getprop ro.build.version.release    # e.g. "15"
adb shell getprop ro.build.version.sdk        # e.g. "35"
adb shell getprop ro.product.model
adb shell getprop ro.product.cpu.abi

# Set system prop (writable props only; useful for feature flags)
adb shell setprop debug.myapp.feature true

# Simulated input (tap, swipe, key events)
adb shell input tap 540 960
adb shell input swipe 540 1400 540 400 300    # swipe up, 300ms
adb shell input keyevent KEYCODE_BACK
adb shell input keyevent KEYCODE_HOME
adb shell input text "hello%sworld"           # %s = space

# Enable/disable WiFi
adb shell svc wifi enable
adb shell svc wifi disable

# Check battery state (useful for power tests)
adb shell dumpsys battery
```

## Instrumented Tests (Espresso / UIAutomator)

```bash
# Run all instrumented tests via Gradle (preferred — handles install automatically)
./gradlew connectedAndroidTest

# Run a specific test class
./gradlew connectedAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.example.LoginTest

# Run a specific test method
./gradlew connectedAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.example.LoginTest#testValidCredentials

# Run directly via adb am instrument (lower-level, useful for debugging)
adb shell am instrument -w \
  -e class com.example.LoginTest \
  com.example.myapp.test/androidx.test.runner.AndroidJUnitRunner

# Pull test results XML
adb pull /sdcard/test-results/ ./test-results/
```

## UIAutomator (UI Inspection)

```bash
# Dump current UI hierarchy to XML
adb shell uiautomator dump /sdcard/ui.xml
adb pull /sdcard/ui.xml ./ui.xml

# View hierarchy as text (API 21+)
adb shell uiautomator dump --compressed /sdcard/ui.xml && adb shell cat /sdcard/ui.xml
```

## Emulator Console (telnet)

```bash
# Connect to emulator console (port printed at emulator launch, e.g. 5554)
telnet localhost 5554

# Inside the console:
# Simulate GPS
geo fix -122.4194 37.7749

# Simulate network conditions
network speed gsm
network speed lte
network delay none

# Simulate battery level
power capacity 20
power status charging

# Quit
quit
```

## CI Integration Pattern

```bash
#!/usr/bin/env bash
# ci-android.sh — start emulator, run tests, collect artifacts

set -euo pipefail

AVD_NAME="${AVD_NAME:-CI_API35}"
PACKAGE="system-images;android-35;google_apis;x86_64"
DEVICE="pixel_6"
APP_PKG="com.example.myapp"

echo "=== Ensuring system image is installed ==="
sdkmanager "$PACKAGE"

echo "=== Creating AVD ==="
avdmanager create avd --name "$AVD_NAME" --package "$PACKAGE" --device "$DEVICE" --force

echo "=== Starting emulator ==="
emulator -avd "$AVD_NAME" -no-window -no-audio -no-boot-anim -gpu swiftshader_indirect &
EMU_PID=$!

echo "=== Waiting for boot ==="
adb wait-for-device
until [[ "$(adb shell getprop sys.boot_completed 2>/dev/null)" == "1" ]]; do sleep 2; done
adb shell input keyevent 82  # dismiss lock screen

echo "=== Running tests ==="
./gradlew connectedAndroidTest || TEST_FAILED=1

echo "=== Collecting artifacts ==="
mkdir -p test-artifacts
adb pull /sdcard/test-results/ test-artifacts/ 2>/dev/null || true
adb logcat -d > test-artifacts/logcat.txt

echo "=== Stopping emulator ==="
adb emu kill
wait "$EMU_PID" 2>/dev/null || true

exit "${TEST_FAILED:-0}"
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `adb: device offline` | ADB server stale | `adb kill-server && adb start-server` |
| Emulator loops on boot | Corrupted snapshot | `emulator -avd NAME -no-snapshot-load` |
| `INSTALL_FAILED_UPDATE_INCOMPATIBLE` | Old signature still installed | `adb uninstall com.pkg` then reinstall |
| `INSTALL_FAILED_NO_MATCHING_ABIS` | APK ABI ≠ emulator ABI | Build for `x86_64`; check `ro.product.cpu.abi` |
| `Could not create the Java Virtual Machine` | Java heap too small | `export _JAVA_OPTIONS="-Xmx4g"` |
| Tests time out waiting for device | Emulator too slow to boot | Use `-no-boot-anim -gpu swiftshader_indirect`; or pre-warm |
| `permission denied` on `/data/data/pkg` | Not running as app UID | Use `adb shell run-as com.pkg` |
| Logcat shows nothing | Wrong filter / buffer cleared | `adb logcat -c` then re-run; check `-s` serial |

## Key File Locations on Device

| Path | Contents |
|---|---|
| `/sdcard/` | External storage (readable without root) |
| `/data/data/<pkg>/` | App private data (accessible via `run-as`) |
| `/data/local/tmp/` | World-writable temp space for pushing test scripts |
| `/sdcard/Android/data/<pkg>/` | App external data (removable) |
| `/proc/net/arp` | ARP table (useful for network debugging) |
