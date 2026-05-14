---
name: expo-mobile
description: Expo and React Native mobile development — Expo CLI, dev server, EAS Build/Submit/Update, Metro bundler, Expo Go, simulators/emulators, native modules, OTA updates, and app store deployment. Triggers "expo", "react native", "eas build", "eas submit", "metro bundler", "expo dev server", "expo go", "expo sdk", "bare workflow", "managed workflow", "app.json", "app.config.js", "expo-router", "expo start".
version: 1.0.0
---
# Expo Mobile Development Skill

Structured workflows for building, running, testing, and shipping React Native apps with Expo. Covers both the **managed workflow** (Expo handles native code) and the **bare workflow** (you own the native projects).

## Core Principles

1. **Always start the dev server first.** `npx expo start` must be running before connecting a device or simulator. It serves the JS bundle and handles hot reload.
2. **Match SDK versions.** The Expo SDK version in `package.json` must match the runtime on the device/simulator. Mismatches cause cryptic red screens.
3. **Use EAS for real device builds.** `expo start` with Expo Go is for development. For TestFlight/Play Store or custom native code, use `eas build`.
4. **Check `app.json` / `app.config.js` first.** Nearly all runtime behaviour (permissions, plugins, splash, icons, bundle ID) is controlled there.
5. **Never modify `ios/` or `android/` directly in managed workflow.** Use config plugins or switch to bare.
6. **OTA updates ship JS only.** Native code changes (new plugins, SDK upgrades) always require a new binary build.

---

## Environment Setup

```bash
# Verify Node and package manager
node --version    # ≥ 18 recommended
npm --version     # or yarn/pnpm/bun

# Install Expo CLI (project-local — preferred)
npm install --save-dev expo

# Or global EAS CLI
npm install -g eas-cli
eas --version

# Log in to Expo account (required for EAS)
eas login
eas whoami
```

### iOS Prerequisites (macOS only)
```bash
# Xcode + command-line tools
xcode-select --install
xcrun simctl list devices   # list available simulators

# CocoaPods (bare workflow)
sudo gem install cocoapods
pod --version
```

### Android Prerequisites
```bash
# Confirm ADB and emulator are on PATH
adb --version
emulator -list-avds

# See android-emulator skill for full AVD/SDK setup
```

---

## Starting the Dev Server

```bash
# Start with interactive menu (choose simulator/device/tunnel)
npx expo start

# Platform-specific shortcuts
npx expo start --ios        # open iOS Simulator immediately
npx expo start --android    # open Android emulator/device immediately
npx expo start --web        # open in browser (Expo Web)

# Clear Metro cache (fixes most "module not found" / stale bundle issues)
npx expo start --clear

# Use tunnel when device and machine are on different networks
npx expo start --tunnel     # requires @expo/ngrok: npx expo install @expo/ngrok

# Run on a specific simulator by UDID
npx expo run:ios --simulator "iPhone 16 Pro"

# Run on Android emulator (must be booted first)
npx expo run:android
```

> **Tip:** Keep the terminal running `expo start` visible. Metro logs and red-screen stack traces appear there, not in the device log.

---

## Project Structure

```
my-app/
├── app/                  # expo-router: file-based routing (if used)
│   ├── (tabs)/
│   │   ├── index.tsx
│   │   └── _layout.tsx
│   └── _layout.tsx
├── components/
├── assets/               # images, fonts, icons
├── app.json              # static config (bundle ID, name, SDK version, permissions)
├── app.config.js         # dynamic config (env vars, conditional plugins)
├── eas.json              # EAS build/submit/update profiles
├── babel.config.js
├── tsconfig.json
├── package.json
├── ios/                  # bare workflow only
└── android/              # bare workflow only
```

### `app.json` Key Fields
```json
{
  "expo": {
    "name": "My App",
    "slug": "my-app",
    "version": "1.0.0",
    "sdkVersion": "52.0.0",
    "orientation": "portrait",
    "icon": "./assets/icon.png",
    "splash": { "image": "./assets/splash.png", "resizeMode": "contain" },
    "ios": {
      "bundleIdentifier": "com.example.myapp",
      "supportsTablet": true,
      "infoPlist": { "NSCameraUsageDescription": "Used for scanning QR codes." }
    },
    "android": {
      "package": "com.example.myapp",
      "permissions": ["CAMERA", "READ_EXTERNAL_STORAGE"],
      "adaptiveIcon": { "foregroundImage": "./assets/adaptive-icon.png" }
    },
    "plugins": [
      "expo-camera",
      ["expo-location", { "locationAlwaysAndWhenInUsePermission": "..." }]
    ]
  }
}
```

---

## Installing and Managing Dependencies

```bash
# Always use `expo install` for Expo/RN-compatible packages — it pins the
# correct version for your SDK. Do NOT use plain `npm install` for Expo deps.
npx expo install expo-camera expo-location expo-router

# Check for outdated or incompatible package versions
npx expo-doctor

# Fix version mismatches (overwrites package.json with compatible versions)
npx expo install --fix

# After adding a new native module in bare workflow, re-run pod install
cd ios && pod install && cd ..
```

---

## Expo Router (File-Based Routing)

```bash
# Create a new Expo Router project
npx create-expo-app@latest my-app --template tabs

# Navigation is derived from the file system:
# app/index.tsx           → /
# app/profile.tsx         → /profile
# app/(tabs)/_layout.tsx  → tab navigator wrapping children
# app/[id].tsx            → /123  (dynamic segment, useLocalSearchParams())
```

```tsx
// Navigate programmatically
import { router } from 'expo-router';
router.push('/profile');
router.replace('/login');
router.back();

// Link component
import { Link } from 'expo-router';
<Link href="/profile">Go to profile</Link>

// Access route params
import { useLocalSearchParams } from 'expo-router';
const { id } = useLocalSearchParams<{ id: string }>();
```

---

## EAS Build

EAS Build compiles native binaries on Expo's cloud infrastructure (or locally).

```bash
# Configure EAS in a project (creates eas.json)
eas build:configure

# Build for a platform
eas build --platform ios          # production iOS .ipa
eas build --platform android      # production Android .aab
eas build --platform all          # both platforms

# Use a specific profile from eas.json
eas build --platform ios --profile preview

# Local build (runs Xcode/Gradle on your machine, no upload)
eas build --platform ios --local
eas build --platform android --local
```

### `eas.json` Build Profiles
```json
{
  "build": {
    "development": {
      "developmentClient": true,
      "distribution": "internal",
      "ios": { "simulator": true }
    },
    "preview": {
      "distribution": "internal",
      "android": { "buildType": "apk" }
    },
    "production": {
      "autoIncrement": true
    }
  }
}
```

### Development Client
A development client is a custom Expo Go that includes your native modules.

```bash
# Build a development client binary
eas build --profile development --platform ios
eas build --profile development --platform android

# After installing on device/simulator, start the dev server in dev-client mode
npx expo start --dev-client
```

---

## EAS Submit (App Store / Play Store)

```bash
# Submit latest build to Apple App Store Connect
eas submit --platform ios --latest

# Submit specific build by ID
eas submit --platform ios --id <build-id>

# Submit to Google Play (requires service account JSON key)
eas submit --platform android --latest
```

### `eas.json` Submit Profiles
```json
{
  "submit": {
    "production": {
      "ios": {
        "appleId": "you@example.com",
        "ascAppId": "1234567890",
        "appleTeamId": "ABCDE12345"
      },
      "android": {
        "serviceAccountKeyPath": "./service-account.json",
        "track": "internal"
      }
    }
  }
}
```

---

## EAS Update (OTA / Over-the-Air)

OTA updates push new JS bundles to already-installed apps without requiring App Store review. Only works for JS/asset changes — never for native changes.

```bash
# Push an update to the "production" channel
eas update --channel production --message "Fix login crash"

# Push to a specific branch
eas update --branch main --message "Hotfix: null check on user profile"

# List recent updates
eas update:list

# Rollback to a previous update group
eas update:rollback --group <update-group-id>
```

### Expo Updates Config in `app.json`
```json
{
  "expo": {
    "updates": {
      "url": "https://u.expo.dev/<project-id>",
      "fallbackToCacheTimeout": 0,
      "checkAutomatically": "ON_LOAD"
    },
    "runtimeVersion": { "policy": "sdkVersion" }
  }
}
```

---

## Metro Bundler

Metro is the JS bundler that `expo start` runs under the hood.

```bash
# Clear Metro cache (fixes most stale-module issues)
npx expo start --clear

# Reset all caches (Metro + watchman + node_modules/.cache)
watchman watch-del-all
rm -rf $TMPDIR/metro-* $TMPDIR/haste-*
npm start -- --reset-cache

# Inspect the bundle (open in browser while dev server is running)
# http://localhost:8081/index.bundle?platform=ios&dev=true&minify=false
```

### Common Metro Config (`metro.config.js`)
```js
const { getDefaultConfig } = require('expo/metro-config');
const config = getDefaultConfig(__dirname);

// Add extra asset extensions
config.resolver.assetExts.push('lottie');

// Monorepo: watch parent packages
config.watchFolders = ['../../packages/shared'];

module.exports = config;
```

---

## Running on Simulators and Devices

### iOS Simulator
```bash
# List available simulators
xcrun simctl list devices available

# Boot a specific simulator (expo start --ios does this automatically)
xcrun simctl boot "iPhone 16 Pro"

# Install a .app bundle manually
xcrun simctl install booted path/to/MyApp.app

# Open Expo Go on a booted simulator
npx expo start --ios
# Press 'i' in the Expo CLI interactive menu

# Take a screenshot
xcrun simctl io booted screenshot screenshot.png

# Stream simulator logs
xcrun simctl spawn booted log stream --predicate 'subsystem == "com.example.myapp"'
```

### Android Emulator
```bash
# List AVDs
emulator -list-avds

# Boot an AVD (see android-emulator skill for full workflow)
emulator -avd Pixel_9_API_35 -no-snapshot-load &

# Wait for boot
adb wait-for-device shell 'while [[ "$(getprop sys.boot_completed)" != "1" ]]; do sleep 2; done'

# Press 'a' in the Expo CLI interactive menu to open on booted emulator
npx expo start --android
```

### Physical Device (Expo Go)
1. Install Expo Go from the App Store / Play Store
2. Run `npx expo start`
3. Scan the QR code with the camera (iOS) or the Expo Go app (Android)
4. For LAN to fail: use `npx expo start --tunnel`

---

## Environment Variables

```bash
# .env (requires expo-constants or react-native-dotenv)
EXPO_PUBLIC_API_URL=https://api.example.com    # exposed to JS (EXPO_PUBLIC_ prefix)
SECRET_KEY=...                                  # NOT exposed; build-time only

# Access in code
import Constants from 'expo-constants';
const apiUrl = process.env.EXPO_PUBLIC_API_URL;

# Pass to EAS builds
eas secret:create --scope project --name SECRET_KEY --value "..."
eas secret:list
```

---

## Debugging

```bash
# Open React Native DevTools (requires Expo SDK 50+)
npx expo start
# Press 'j' in the CLI menu to open Chrome DevTools / Hermes debugger

# Hermes debugger URL (when dev server is running)
# chrome://inspect  →  "Open dedicated DevTools for Node"

# Element inspector: shake device or press Cmd+D (iOS Sim) / Cmd+M (Android)
# → Toggle Element Inspector

# Flipper (older projects)
# Install Flipper desktop app, then run in dev mode

# View crash logs
# iOS: Console.app → filter by device → look for process "MyApp"
# Android: adb logcat -s ReactNativeJS:V
adb logcat -s ReactNativeJS:V ReactNative:V

# JavaScript console logs via adb
adb logcat | grep "console\."
```

---

## Testing

```bash
# Unit tests with Jest (bundled in Expo projects)
npx jest
npx jest --watch
npx jest --coverage

# Component tests with React Native Testing Library
npm install --save-dev @testing-library/react-native

# E2E tests with Maestro (recommended for RN)
# Install: curl -Ls "https://get.maestro.mobile.dev" | bash
maestro test flow.yaml

# E2E with Detox (older standard)
npx detox build --configuration ios.sim.debug
npx detox test --configuration ios.sim.debug
```

---

## Common Issues and Fixes

| Symptom | Likely Cause | Fix |
|---|---|---|
| Red screen "Unable to resolve module X" | Missing dependency or Metro cache | `npx expo install X` then `expo start --clear` |
| "SDK version mismatch" | Expo Go version ≠ project SDK | Update Expo Go app or run `npx expo install --fix` |
| `pod install` fails | Missing CocoaPods or Xcode | `sudo gem install cocoapods && pod repo update` |
| Build fails: "No bundle identifier" | `ios.bundleIdentifier` missing in app.json | Add it to `app.json` and re-run |
| `eas build` says "not logged in" | Not authenticated | `eas login` |
| OTA update not appearing | Native code change shipped as OTA | Must publish full binary build |
| Hot reload stops working | Metro watchman stale | `watchman watch-del-all` then restart |
| "Network request failed" on device | Device can't reach metro host | Use `npx expo start --tunnel` |
| Android build: "SDK location not found" | `ANDROID_HOME` not set | `export ANDROID_HOME=$HOME/Android/Sdk` |
| `expo-doctor` reports errors | Version incompatibilities | `npx expo install --fix` |

---

## Key File Locations

| File | Purpose |
|---|---|
| `app.json` / `app.config.js` | App config, permissions, plugins, SDK version |
| `eas.json` | EAS build/submit/update profiles |
| `babel.config.js` | JS transpile config |
| `metro.config.js` | Metro bundler config |
| `tsconfig.json` | TypeScript config (extend `expo/tsconfig.base`) |
| `ios/Podfile` | CocoaPods dependencies (bare workflow) |
| `android/build.gradle` | Gradle config (bare workflow) |
| `.env` | Local env vars (not committed) |
| `~/.expo/` | Expo CLI credentials and session cache |
