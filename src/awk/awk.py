"""
Date: 2020.12.02
"""

import sys
import os
import re


def find_pattern(pattern, filename):
    if not os.path.exists(filename):
        print(filename, " is not exist!")
        return

    if not os.path.isfile(filename):
        print(filename, " is not file!")
        return

    print(filename, " : ", pattern, "\n---")

    with open(filename, "rt", encoding="UTF-8", errors="surrogatepass") as targetFile:
        for line in targetFile:
            is_exist = re.search(pattern, line)
            if is_exist is not None:
                print(line[:-1])
    print("\n")


def main(argv):
    pattern = argv[0]

    if "/'" in pattern:
        find_pattern(pattern[2:-2], argv[1])
    else:
        for i in range(1, len(argv) - 1):
            pattern += argv[i]
            print(pattern, ':', argv[i])
        find_pattern(pattern[2:-2], argv[-1])


if __name__ == '__main__':
    if len(sys.argv) < 2:
        print("Usage: awk [option] pattern filename")
    else:
        main(sys.argv[1:])
