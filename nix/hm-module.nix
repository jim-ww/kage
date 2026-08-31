{
  self,
}:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.kage;
  yamlFormat = pkgs.formats.yaml { };
in
{
  options.programs.kage = {
    enable = lib.mkEnableOption "kage, a TUI XMPP client";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "kage.packages.<system>.default";
      description = "The kage package to install.";
    };

    settings = lib.mkOption {
      type = yamlFormat.type;
      default = { };
      description = ''
        Settings written verbatim to config.yaml (keys as documented in
        kage's config.Config, e.g. `mouse_disabled`, `theme`, `keybinds`,
        `accounts`, `storage`, ...). This is only the declarative half of
        kage's config: settings the app itself mutates at runtime (dragged
        sidebar width, last opened chat, cycled sort order, per-account
        presence, ...) live in a separate state.yaml next to config.yaml
        that this module never touches - see config.State in kage's source
        for the full list.

        Note: any `password`/`password_cmd`/`storage.password` set here is
        copied verbatim into the Nix store (world-readable on multi-user
        systems). Prefer `password_cmd` pointing at a secret manager (pass,
        sops-nix, the OS keyring, ...) over a literal `password`.
      '';
      example = lib.literalExpression ''
        {
          mouse_disabled = true;
          default_encryption_mode = "omemo-v2";
          theme.app_bg = "#000000";
          keybinds.quit = [ "q" "ctrl+c" ];
          accounts = [
            {
              jid = "me@example.org";
              password_cmd = "cat ''${config.sops.secrets.kage-xmpp-password.path}";
            }
          ];
        }
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."kage/config.yaml" = lib.mkIf (cfg.settings != { }) {
      source = yamlFormat.generate "kage-config.yaml" cfg.settings;
    };
  };
}
