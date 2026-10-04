{ pkgs, ... }:
{
  packages = [
    pkgs.go_1_27
    pkgs.gopls
    pkgs.gotools
    pkgs.golangci-lint
    pkgs.just
    pkgs.git
  ];

  enterShell = ''
    go version
    golangci-lint version
  '';

  enterTest = ''
    go test ./...
  '';
}
