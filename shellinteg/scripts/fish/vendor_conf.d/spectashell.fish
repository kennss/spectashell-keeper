# spectashell.fish
# Author: Kennt Kim
# Created: 2026-10-19 · Last updated: 2026-11-02
#
# Purpose: the terminal engine's shell integration for fish (SiliconDesk's Docs/SDTERM-2.md §4.3,
# PROTOCOL.md §7.7). The session starts fish with this directory first in XDG_DATA_DIRS, so fish
# reads this file from vendor_conf.d before the user's config. It takes the directory out of
# XDG_DATA_DIRS again (and erases the variable when the session had to make it up), reports the
# working directory with OSC 7, and — on fish 3, which does not mark its prompts itself — marks each
# prompt, command and end with OSC 133.
#
# Notes:
# - Taken from SiliconDesk's host/internal/shellinteg/scripts/fish/vendor_conf.d/sdterm.fish on
#   2026-10-23. Where these notes say "the session", read whoever starts the shell: the
#   engine's user (Docs/design/02-CONNECTIONS.md §1).
# - fish 4 prints OSC 133 itself; its marks are left alone.
# - C carries the command line (`cmdline_url`); fish has no cheap clock, so not its start: a command
#   that runs across a session change ends with no known duration.
# - aid is the shell's pid: an inner shell's marks (ssh to a host with integration) are told apart.
# - The directory is reported at every prompt as well as when it changes: a prompt's mark carries
#   the last one reported, and after `ssh` to a host whose shell reported its own, this shell's
#   prompts would otherwise have none.

if set -q SPECTASHELL_SHELL_INTEGRATION_DIR
    if set -q SPECTASHELL_XDG_DATA_DIRS_UNSET
        set -e XDG_DATA_DIRS
        set -e SPECTASHELL_XDG_DATA_DIRS_UNSET
    else
        set -gx XDG_DATA_DIRS (string join : (string match -v -- $SPECTASHELL_SHELL_INTEGRATION_DIR (string split : -- $XDG_DATA_DIRS)))
    end
    set -e SPECTASHELL_SHELL_INTEGRATION_DIR
end

if status is-interactive
    function __spectashell_report_cwd --on-variable PWD --on-event fish_prompt
        printf '\e]7;file://%s%s\a' $hostname (string escape --style=url -- $PWD)
    end
    __spectashell_report_cwd

    if test (string split . -- $version)[1] -lt 4
        function __spectashell_prompt --on-event fish_prompt
            printf '\e]133;A;aid=%s\a' $fish_pid
        end
        function __spectashell_preexec --on-event fish_preexec
            printf '\e]133;C;aid=%s;cmdline_url=%s\a' $fish_pid (string escape --style=url -- "$argv")
        end
        function __spectashell_postexec --on-event fish_postexec
            printf '\e]133;D;%s;aid=%s\a' $status $fish_pid
        end
    end
end
