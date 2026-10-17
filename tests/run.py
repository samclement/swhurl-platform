"""Run the unit tests in parallel, one test class per job (make test).

Most of the suite's time is spent waiting on subprocesses (make, bash, fake
kubectl), so a process pool sized to the CPU count cuts wall time. A class is the
unit so setUpClass runs once. Each class's output is buffered and failures are printed whole after the run.
It prints the slowest classes, so a slow one is seen before it slows every run.
Usage: python tests/run.py [-j N] [-v] [-k PATTERN]
"""
import argparse
import io
import os
import sys
import time
import unittest
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

TESTS = Path(__file__).resolve().parent


def _classes(suite):
    """Dotted names of the test classes, in discovery order."""
    seen = {}
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            seen.update(dict.fromkeys(_classes(test)))
        else:
            seen[test.id().rsplit('.', 1)[0]] = None
    return list(seen)


def _init():
    sys.path.insert(0, str(TESTS))


def _run(name):
    stream, start = io.StringIO(), time.monotonic()
    result = unittest.TextTestRunner(stream=stream, verbosity=2).run(
        unittest.defaultTestLoader.loadTestsFromName(name))
    ok = result.wasSuccessful()
    return name, ok, result.testsRun, len(result.skipped), '' if ok else stream.getvalue(), time.monotonic() - start


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('-j', type=int, default=os.cpu_count() or 1)
    parser.add_argument('-v', action='store_true', help='print every test class as it finishes')
    parser.add_argument('-k', default='', help='run only the classes whose dotted name contains this (test_console, Promotion)')
    parser.add_argument('--slowest', type=int, default=8, help='how many of the slowest classes to list')
    args = parser.parse_args()

    _init()
    loader = unittest.TestLoader()
    suite = loader.discover(str(TESTS), top_level_dir=str(TESTS))
    # A module that fails to import cannot be named for a worker: report it here.
    names = [n for n in _classes(suite) if not n.startswith('unittest.loader.') and args.k in n]
    if not names:
        print(f'no test class matches {args.k!r}')
        return 1
    start, ran, skipped, failures, times = time.monotonic(), 0, 0, list(loader.errors), []
    with ProcessPoolExecutor(max_workers=args.j, initializer=_init) as pool:
        for name, ok, count, skips, output, seconds in pool.map(_run, names):
            ran, skipped = ran + count, skipped + skips
            times.append((seconds, name))
            if args.v:
                print(f'{name} ... {"ok" if ok else "FAIL"}', flush=True)
            if not ok:
                failures.append(output)
    for output in failures:
        print(output)
    if len(times) > 1 and args.slowest:
        print('Slowest classes:')
        for seconds, name in sorted(times, reverse=True)[:args.slowest]:
            print(f'  {seconds:6.1f}s  {name}')
    summary = f'Ran {ran} tests in {time.monotonic() - start:.1f}s with {args.j} workers'
    print(summary + (f' (skipped={skipped})' if skipped else ''))
    print(f'FAILED ({len(failures)} failing)' if failures else 'OK')
    return 1 if failures else 0


if __name__ == '__main__':
    sys.exit(main())
