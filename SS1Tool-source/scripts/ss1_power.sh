#!/bin/bash
# SS1 Tool power watch: notices when the SuperStation lost power in the middle of heavy work.
#
# A power adapter that can't keep up resets the SuperStation instantly under load: no crash, no
# error, no log. So this watches for heavy work instead. While Update All, the downloader, Console
# Mode's updater, a backup or a file system check runs, a small marker file says so (written when
# the work starts, removed when it ends). At the next start, a marker from an earlier boot means
# the power went in the middle of that work, unless the work shows it finished (Update All's
# "Success!", the downloader's last-successful-run file) or the earlier boot ended with a normal
# shutdown or restart. Those events are kept in events.log, where SS1 Tool reads them.
#
# Switching off while nothing heavy runs is never counted. While a game or core is running the
# watch only sleeps (scripts and updates run from the menu).
#
#   ss1_power.sh start   from linux/user-startup.sh: the start-up check, then the watch
#   ss1_power.sh stop    from linux/user-startup.sh (and SS1 Tool's shutdown.sh): a normal end
#   ss1_power.sh mark <what> / done <what>   for jobs that want to say so themselves
#   ss1_power.sh status  key=value for SS1 Tool
#
# Installed by SS1 Tool (Diag > Power) and removed there; it changes nothing else on the system.

VERSION=1
D="${SS1_POWER_DIR:-/media/fat/Scripts/.ss1tool/power}"
BUSY="$D/busy"
EVENTS="$D/events.log"
CLEAN="$D/clean"
PAT="$D/patterns"
LOCK=/tmp/ss1_power.lock
INTERVAL="${SS1_POWER_INTERVAL:-10}"

boot_id() { cat /proc/sys/kernel/random/boot_id 2>/dev/null; }
now() { date +%s; }
kv() { sed -n "s/^$2=//p" "$1" 2>/dev/null | head -n 1; }

# what each kind of heavy work looks like in the process list (read from a file, so the search
# doesn't find itself), and its name in the events
write_patterns() {
	mkdir -p "$D" 2>/dev/null
	[ -s "$PAT" ] && [ "$(head -n 1 "$PAT")" = "# v$VERSION" ] && return
	cat > "$PAT.tmp" <<'EOF'
# v1
update_all
downloader
Install_Console_Mode
fsck\.
e2fsck
ntfsfix
SS1_backup
tar -cf - --
EOF
	mv -f "$PAT.tmp" "$PAT"
}

job_name() {
	case "$1" in
		*update_all*) echo "Update All" ;;
		*downloader*) echo "MiSTer's downloader" ;;
		*Install_Console_Mode*) echo "Console Mode update" ;;
		*fsck*|*ntfsfix*) echo "File system check" ;;
		*SS1_backup*) echo "Backup to the NVMe drive" ;;
		*"tar -cf"*) echo "SD card backup to a PC" ;;
		*) echo "Heavy work" ;;
	esac
}

job_key() { echo "$1" | tr -c 'A-Za-z0-9' '_' | cut -c1-40; }

# the work finished even though its marker is still there (it restarted the SuperStation itself)
finished() { # <name> <started at>
	local f
	case "$1" in
		"Update All")
			f=/media/fat/Scripts/.config/update_all/update_all.log
			[ -f "$f" ] && [ "$(stat -c %Y "$f")" -ge "$2" ] && tail -n 20 "$f" | grep -q 'Success!' && return 0 ;;
		"MiSTer's downloader")
			f=/media/fat/Scripts/.config/downloader/downloader.last_successful_run
			[ -f "$f" ] && [ "$(stat -c %Y "$f")" -ge "$2" ] && return 0 ;;
	esac
	return 1
}

# at start-up: markers left by an earlier boot that didn't end normally
check() {
	local cur clean m b at what jobs="" first=0 lost_boot=""
	cur="$(boot_id)"
	[ -d "$BUSY" ] || return 0
	clean="$(kv "$CLEAN" boot)"
	for m in "$BUSY"/*; do
		[ -f "$m" ] || continue
		b="$(kv "$m" boot)"; at="$(kv "$m" at)"; what="$(kv "$m" what)"
		[ "$b" = "$cur" ] && continue
		rm -f "$m"
		case "$at" in ''|*[!0-9]*) at=0 ;; esac
		[ -n "$b" ] && [ "$b" = "$clean" ] && continue
		finished "$what" "$at" && continue
		# one power cut is one event, however many jobs it interrupted
		jobs="${jobs:+$jobs, }$what"
		{ [ "$first" -eq 0 ] || { [ "$at" -gt 0 ] && [ "$at" -lt "$first" ]; }; } && first="$at"
		lost_boot="$b"
	done
	[ -n "$jobs" ] && echo "$first|$jobs|$lost_boot" >> "$EVENTS"
	[ "$clean" = "$cur" ] || rm -f "$CLEAN"
	sync
}

mark() { # <what>
	local k
	k="$(job_key "$1")"
	mkdir -p "$BUSY" 2>/dev/null
	[ -f "$BUSY/$k" ] && [ "$(kv "$BUSY/$k" boot)" = "$(boot_id)" ] && return 0
	printf 'boot=%s\nat=%s\nwhat=%s\n' "$(boot_id)" "$(now)" "$1" > "$BUSY/$k.tmp" && mv -f "$BUSY/$k.tmp" "$BUSY/$k"
	sync
}

done_() { # <what>
	local k
	k="$(job_key "$1")"
	[ -f "$BUSY/$k" ] || return 0
	rm -f "$BUSY/$k"
	sync
}

watch() {
	local core m w n a
	while :; do
		sleep "$INTERVAL"
		# a game or core is running: nothing to watch (scripts and updates run from the menu)
		core="$(cat /tmp/CORENAME 2>/dev/null)"
		[ -n "$core" ] && [ "$core" != MENU ] && continue
		# one search of every process's command line; a shell running a one-line command ("sh -c ...",
		# like SS1 Tool's own checks) isn't the work itself and is skipped
		declare -A on=()
		for m in $(grep -a -l -E -f <(grep -v '^#' "$PAT") /proc/[0-9]*/cmdline 2>/dev/null); do
			mapfile -d '' -t a < "$m" 2>/dev/null || continue
			[ "${a[1]:-}" = "-c" ] && continue
			n="$(job_name "${a[*]}")"
			on["$n"]=1
		done
		for n in "${!on[@]}"; do mark "$n"; done
		# work that ended: its marker goes
		for m in "$BUSY"/*; do
			[ -f "$m" ] || continue
			[ "$(kv "$m" boot)" = "$(boot_id)" ] || continue
			w="$(kv "$m" what)"
			[ -n "${on[$w]:-}" ] || [ "$(kv "$m" self)" = 1 ] || done_ "$w"
		done
		unset on
	done
}

case "${1:-}" in
	start)
		write_patterns
		exec 9>"$LOCK"
		flock -n 9 || exit 0
		check
		( trap '' HUP; exec nice -n 19 "$0" run 9>&- ) </dev/null >/dev/null 2>&1 &
		;;
	run)
		exec 8>"$LOCK.run"
		flock -n 8 || exit 0
		watch
		;;
	stop)
		mkdir -p "$D" 2>/dev/null
		printf 'boot=%s\nat=%s\n' "$(boot_id)" "$(now)" > "$CLEAN.tmp" && mv -f "$CLEAN.tmp" "$CLEAN"
		sync
		;;
	mark)
		[ -n "${2:-}" ] && { mark "$2"; printf 'self=1\n' >> "$BUSY/$(job_key "$2")"; }
		;;
	done)
		[ -n "${2:-}" ] && done_ "$2"
		;;
	status)
		echo "version=$VERSION"
		for p in /proc/[0-9]*/cmdline; do case "$(tr '\0' ' ' < "$p" 2>/dev/null)" in *"ss1_power.sh run "*) echo running=1; break ;; esac; done
		ls "$BUSY" 2>/dev/null | sed 's/^/busy=/'
		[ -f "$EVENTS" ] && sed 's/^/event=/' "$EVENTS"
		;;
	*)
		sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
		exit 2
		;;
esac
