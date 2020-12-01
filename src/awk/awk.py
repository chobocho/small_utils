"""
Date: 2020.12.02
"""

import sys
import os
import re
import glob

def __is_correct_path(filename):
    if not os.path.exists(filename):
        print(filename, " is not exist!")
        return False

    if not os.path.isfile(filename):
        print(filename, " is not file!")
        return False

    return True


def check_pattern(pattern, filename):
    try:
        with open(filename, "rt", encoding="UTF-8", errors="surrogatepass") as targetFile:
            for line in targetFile:
                is_exist = re.search(pattern, line)
                if is_exist is not None:
                    print(line[:-1])
    except:
        pass


def find_pattern(pattern, filename):
    if (filename == ".") or (filename == ".."):
        print("Not support ", filename)
        return False

    folder_list = glob.glob(filename)

    if (len(folder_list) == 0) and (not __is_correct_path(filename)):
        return

    if len(folder_list) == 0:
        print(filename, " : ", pattern, "\n---")
        check_pattern(pattern, filename)
        print("\n")
    else:
        for fn in folder_list:
            print(fn, " : ", pattern, "\n---")
            check_pattern(pattern, fn)
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
    if len(sys.argv) < 3:
        print("Usage: awk [option] pattern filename")
    else:
        main(sys.argv[1:])
