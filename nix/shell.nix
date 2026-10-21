{
  perSystem =
    { pkgs, self', ... }:
    {
      devShells.default = pkgs.mkShell {
        # The package's build inputs carry its Go toolchain, so the shell
        # compiles with exactly the go the flake builds with.
        inputsFrom = [ self'.packages.apm ];
        packages = [
          pkgs.gopls
          pkgs.gotools
          pkgs.golangci-lint
          pkgs.just
          pkgs.git
        ];
      };

      formatter = pkgs.nixpkgs-fmt;
    };
}
