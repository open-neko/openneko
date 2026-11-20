"""Usage: native-zip.py <tree> <archive.zip> | native-zip.py --list <archive.zip>"""
import os
import sys
import zipfile


def write(root, dst):
    with zipfile.ZipFile(dst, "w", zipfile.ZIP_DEFLATED) as z:
        for top in ("bin", "share"):
            for folder, dirs, files in os.walk(os.path.join(root, top)):
                dirs.sort()
                rel = os.path.relpath(folder, root).replace(os.sep, "/")
                z.write(folder, rel + "/")
                for name in sorted(files):
                    z.write(os.path.join(folder, name), rel + "/" + name)


if sys.argv[1] == "--list":
    print("\n".join(zipfile.ZipFile(sys.argv[2]).namelist()))
else:
    write(sys.argv[1], sys.argv[2])
