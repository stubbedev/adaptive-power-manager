{
  description = "Adaptive battery charging and power profile policy for Linux laptops";

  # Offer the prebuilt CI cache so consumers fetch the binary instead of
  # rebuilding it. Honoured with --accept-flake-config (or for trusted users).
  # The cache is public; no token needed to pull.
  nixConfig = {
    extra-substituters = [ "https://nix.stubbe.dev/default" ];
    extra-trusted-public-keys = [ "default:9P4FePqHV1rGv5NDBun0GN26y83pcaaMr/NHZrxKaac=" ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      # The daemon reads Linux sysfs and talks to system D-Bus services; a
      # darwin build would compile and be useless.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      imports = [
        ./nix/package.nix
        ./nix/shell.nix
      ];
    };
}
