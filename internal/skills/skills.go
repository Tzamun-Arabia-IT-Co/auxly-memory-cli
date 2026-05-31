// Package skills holds the Auxly skills version, intentionally decoupled from
// the tool's release VERSION.
package skills

// Version is the Auxly skills serial (semver). Bump it by significance whenever a
// SKILL.md body changes — and ONLY then, never on an unrelated tool release.
//
// Why decoupled: the tool ships many releases where the skills are untouched;
// re-downloading identical skills each time is noise. Conversely, a skill can
// change without a tool release and MUST trigger a fresh download. So the serial
// tracks skill CONTENT, not the binary.
//
// Why it lives in the folder name, not the .zip name: Claude identifies a skill
// by its .zip filename, so renaming the zip would make Claude treat an update as
// a brand-new skill (the user would have to delete the old one by hand). The
// download FOLDER (~/Downloads/auxly-skills-v<Version>/) carries the version
// instead — Claude never imports the folder, a new version lands as a new folder
// (no collision, visible to the user), and the zips inside keep stable names so
// Claude replaces the skill in place.
const Version = "1.0.0"
