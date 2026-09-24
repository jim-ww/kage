.PHONY: shell
shell:
	nix-shell -p prosody coturn openssl libnotify alsa-lib pkg-config wf-recorder mpv
