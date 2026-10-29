# sdterm.bash
# Author: Kennt Kim
# Created: 2026-10-19 · Last updated: 2026-10-30
#
# Purpose: the terminal engine's shell integration for bash (SiliconDesk's SDTERM-2.md §4.3,
# PROTOCOL.md §7.7): marks each prompt, command line, command output and command end with OSC 133,
# and reports the working directory with OSC 7, so sdterm can jump between prompts, copy a command's
# output and say when a long command ends. Nothing in the user's files is changed.
#
# Notes:
# - Taken from SiliconDesk's host/internal/shellinteg/scripts/bash/sdterm.bash on
#   2026-10-23. Where these notes say "the session", read whoever starts the shell: the
#   engine's user (Docs/design/02-CONNECTIONS.md §1).
# - The session starts the login shell as `-bash --posix` with ENV naming this file and
#   SDTERM_BASH_INJECT set: in POSIX mode an interactive bash reads only $ENV, so this file runs
#   first. Sourced from a ~/.bashrc (another host's) it only sets the marks. It ends POSIX mode and reads the
#   login files as bash would have (/etc/profile, then the first of ~/.bash_profile,
#   ~/.bash_login, ~/.profile), as kitty's integration does. HISTFILE was exported by the session
#   (POSIX mode would otherwise use ~/.sh_history) and is unexported again here.
# - Marks need bash 4.4 (PS0). bash has no preexec: C is printed from PS0, which runs after a line
#   is read and before it runs, and not for Ctrl-C at the prompt (a cancelled line). D;$? is the
#   first thing PROMPT_COMMAND runs, before anything that would change $?, and it hands $? on. A
#   and B are put around PS1 again at every prompt by the last entry of PROMPT_COMMAND, since conda
#   and starship rewrite PS1 after the startup files. PROMPT_COMMAND is added to, never replaced.
#   No DEBUG trap: it would fight the user's own and bash-preexec's.
# - C carries the command's start (`sdterm_started`, Unix milliseconds from EPOCHREALTIME, bash 5)
#   so its duration survives a session change. The command line is read from the screen by the
#   terminal (bash gives no hook with it).
# - aid is the shell's pid: an inner shell's marks (ssh to a host with integration) are told apart.
# - The directory is reported at every prompt, not only when it changes: a prompt's mark carries the
#   last one reported (paths in a command's output are read from it), and after `ssh` to a host
#   whose shell reported its own, this shell's prompts would otherwise have none.
# - A prompt that marks itself already (`]133;` in PS1 or PS0) is left alone.

if [[ "$-" != *i* ]]; then builtin return 2>/dev/null; fi

# Started by the session (SDTERM_BASH_INJECT): leave POSIX mode and read the startup files it
# skipped. Sourced from a ~/.bashrc instead (another host's, "Install Shell Integration on This
# Host…"), only the marks below are set.
if [[ -n "${SDTERM_BASH_INJECT-}" ]]; then
    builtin unset SDTERM_BASH_INJECT ENV
    if [[ -n "${SDTERM_ENV+x}" ]]; then
        builtin export ENV="$SDTERM_ENV"
        builtin unset SDTERM_ENV
    fi
    builtin set +o posix
    builtin shopt -u inherit_errexit 2>/dev/null
    if [[ -n "$SDTERM_UNEXPORT_HISTFILE" ]]; then
        builtin export -n HISTFILE
        builtin unset SDTERM_UNEXPORT_HISTFILE
    fi

    # The startup files POSIX mode skipped (bash's run_startup_files, for a login shell).
    if builtin shopt -q login_shell; then
        if [[ -r /etc/profile ]]; then builtin source /etc/profile; fi
        for _sdterm_file in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
            if [[ -r "$_sdterm_file" ]]; then
                builtin source "$_sdterm_file"
                break
            fi
        done
        builtin unset _sdterm_file
    fi
fi

if (( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 4) )) &&
    [[ "$PS1$PS0" != *']133;'* && -z "${_sdterm_aid-}" ]]; then
    _sdterm_aid=$$
    _sdterm_a=$'\e]133;A;aid='"$_sdterm_aid"$'\a'
    _sdterm_b=$'\e]133;B\a'
    if [[ -n "${EPOCHREALTIME-}" ]]; then
        _sdterm_c=$'\e]133;C;aid='"$_sdterm_aid"';sdterm_started=$(( ${EPOCHREALTIME//[!0-9]/} / 1000 ))'$'\a'
    else
        _sdterm_c=$'\e]133;C;aid='"$_sdterm_aid"$'\a'
    fi

    # Percent-encodes $1 into _sdterm_url (a path for OSC 7).
    _sdterm_urlencode() {
        builtin local LC_ALL=C s="$1" out="" c i
        for (( i = 0; i < ${#s}; i++ )); do
            c="${s:i:1}"
            case "$c" in
                [a-zA-Z0-9._~/-]) out+="$c" ;;
                *) builtin printf -v c '%%%02X' "'$c"; out+="$c" ;;
            esac
        done
        _sdterm_url="$out"
    }

    # First in PROMPT_COMMAND: the last command's end, and the directory (encoded again only when
    # it changed).
    _sdterm_precmd() {
        builtin local status=$?
        builtin printf '\e]133;D;%s;aid=%s\a' "$status" "$_sdterm_aid"
        if [[ "$PWD" != "${_sdterm_pwd-}" ]]; then
            _sdterm_pwd="$PWD"
            _sdterm_urlencode "$PWD"
            _sdterm_pwd_url="$_sdterm_url"
        fi
        builtin printf '\e]7;file://%s%s\a' "$HOSTNAME" "$_sdterm_pwd_url"
        builtin return "$status"
    }

    # Last in PROMPT_COMMAND: A and B around PS1, C at the end of PS0, wherever others moved them.
    _sdterm_prompt() {
        builtin local status=$?
        PS1="${PS1//"\[$_sdterm_a\]"/}"
        PS1="${PS1//"\[$_sdterm_b\]"/}"
        PS1="\[$_sdterm_a\]$PS1\[$_sdterm_b\]"
        PS0="${PS0//"$_sdterm_c"/}$_sdterm_c"
        builtin return "$status"
    }

    if (( BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1) )); then
        PROMPT_COMMAND=(_sdterm_precmd "${PROMPT_COMMAND[@]}" _sdterm_prompt)
    else
        PROMPT_COMMAND="_sdterm_precmd"$'\n'"${PROMPT_COMMAND:+$PROMPT_COMMAND$'\n'}_sdterm_prompt"
    fi
fi
