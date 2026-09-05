// Build the engine, with the OAuth client sealed in when the build was given
// one.
//
// This exists so that `npm run build:daemon` can do one conditional thing that
// an npm script string cannot: when GOOGLE_OAUTH_CLIENT_ID and
// GOOGLE_OAUTH_CLIENT_SECRET are in the environment -- which, in practice,
// means the release workflow and its two GitHub secrets -- seal them and link
// the result in. With neither set it runs exactly the `go build` it always
// ran, and the engine ships with no built-in sign-in, as a clone of this
// repository always will.
//
// The Makefile deliberately does not go through here. The engine has to stay
// buildable and runnable with no Node anywhere near it, so `make engine` gets
// the plain build; a build with a credential in it is a release, and a release
// goes through npm.
//
// Nothing in here prints the blob, the id or the secret. GitHub masks the two
// secrets in a log, but it has never seen the sealed blob and would not mask
// it, and a public build log is as public as the source tree.

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";

const ENGINE = "dist/mikkilensd.exe";

// The one -X path that has to be exactly right. Get it wrong -- rename the
// package, move the file, typo the variable -- and `go build` says nothing at
// all: it accepts an -X for a symbol that does not exist, links a binary with
// the variable still empty, and the first anyone knows is a Connect button
// that does nothing. checkTheCredentialLandedIn below is what turns that
// silence into a failed build.
const EMBEDDED = "github.com/exzork/mikkilens/packages/controllers/youtube.embeddedClient";

// cgo links with the system C toolchain for the wake word's ONNX Runtime, and
// an old MinGW puts its DWARF sections outside the image, which Windows then
// refuses to load. Stripping them keeps Go's own symbols and panic traces.
const ldflags = ["-extldflags=-Wl,--strip-debug"];

const blob = sealTheOAuthClient();
if (blob) {
  ldflags.push(`-X ${EMBEDDED}=${blob}`);
}

// An argv array rather than a command line: the blob is base64url and needs no
// quoting, but building the string by hand is how that stops being true the
// day something else joins it.
const build = spawnSync(
  "go",
  ["build", "-ldflags", ldflags.join(" "), "-o", ENGINE, "./apps/daemon"],
  { stdio: "inherit" },
);

if (build.error) {
  console.error(`Could not run go: ${build.error.message}`);
  process.exit(1);
}
if (build.status !== 0) {
  process.exit(build.status ?? 1);
}

if (blob) {
  checkTheCredentialLandedIn(ENGINE);
}

/**
 * Confirm the credential went in sealed, and went in at all.
 *
 * Two separate checks, for two separate silent failures.
 *
 * The symbol has to exist under exactly the name -X was given. The linker
 * accepts an -X for a symbol that is not there and says nothing, so a renamed
 * variable or a moved package produces a clean build with the credential
 * silently absent -- a release where Connect does nothing. `go tool nm` lists
 * what the binary actually has, which settles it.
 *
 * Searching the binary for the blob would not settle it: Go records the whole
 * -ldflags string in the build info, so the blob is in there whether the
 * linker used it or not. (That also means `go version -m` on the installer
 * prints the sealed blob. It stays sealed, so this is a place it can be found
 * rather than a place it can be read.)
 *
 * Then the plain id and secret must not appear as readable text. That is the
 * same `strings` anyone else would run on the release, run here first, while
 * it can still stop the build.
 */
function checkTheCredentialLandedIn(path) {
  const symbols = spawnSync("go", ["tool", "nm", path], {
    encoding: "utf8",
    // The engine's symbol table runs to tens of megabytes, well past the 1 MB
    // spawnSync buffers by default -- which truncates the output and fails the
    // check for a reason that has nothing to do with the credential.
    maxBuffer: 256 * 1024 * 1024,
  });

  // nm is a check, not the build. If it cannot run at all, say so and carry
  // on rather than failing a release over a missing dev tool.
  if (symbols.error || symbols.status !== 0) {
    console.warn(
      `Could not run 'go tool nm' to verify the credential in ${path}: ` +
        `${symbols.error?.message ?? `exit ${symbols.status}`}`,
    );
  } else if (!symbols.stdout.includes(EMBEDDED)) {
    console.error(
      `${path} has no symbol named\n  ${EMBEDDED}\n` +
        `so the -X that carries the OAuth client went nowhere and this build\n` +
        `would ship without a sign-in. Update EMBEDDED in this script to match\n` +
        `packages/controllers/youtube/embedded.go.`,
    );
    process.exit(1);
  }

  const binary = readFileSync(path);
  for (const name of ["GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET"]) {
    const value = (process.env[name] ?? "").trim();
    if (value && binary.includes(value)) {
      // The value is deliberately not printed. It is already in the
      // executable; putting it in a public build log as well finishes the job.
      console.error(
        `${name} appears as readable text in ${path}. Something is embedding\n` +
          `it unsealed. Do not publish this build.`,
      );
      process.exit(1);
    }
  }

  console.log("The engine carries a sealed YouTube sign-in credential.");
}

/**
 * The sealed OAuth client, or "" when this build was given none.
 *
 * packclient prints the blob on stdout and everything else on stderr, so
 * stdout is taken whole and stderr is passed straight through to the log.
 */
function sealTheOAuthClient() {
  if (!process.env.GOOGLE_OAUTH_CLIENT_ID && !process.env.GOOGLE_OAUTH_CLIENT_SECRET) {
    return "";
  }

  const packed = spawnSync("go", ["run", "./tools/packclient"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "inherit"],
  });

  if (packed.error) {
    console.error(`Could not run packclient: ${packed.error.message}`);
    process.exit(1);
  }
  // packclient has already said why on stderr. Failing here rather than
  // carrying on is the point: a release that was meant to carry a sign-in and
  // quietly did not is a release that gets found by her, on stream.
  if (packed.status !== 0) {
    process.exit(packed.status ?? 1);
  }

  return packed.stdout.trim();
}
