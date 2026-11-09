#include "bundle.h"
#include <iostream>
int main(int argc, char** argv) {
  if (argc < 6 || (argc - 4) % 2) { std::cerr << "usage: sign-bundle APP CERT KEY PROFILE ENTITLEMENTS [PROFILE ENTITLEMENTS...]\n"; return 2; }
  std::list<ZSignAsset> assets;
  for (int i = 4; i < argc; i += 2) {
    assets.emplace_back();
    if (!assets.back().Init(argv[2], argv[3], argv[i], argv[i+1], "", false, true, false)) return 1;
  }
  ZBundle bundle;
  return bundle.SignFolder(&assets, argv[1], "", "", "", {}, {}, true, false, false) ? 0 : 1;
}
