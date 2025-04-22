import minidisc
import unittest

class TestIpnStatus(unittest.TestCase):

    def test_works(self):
        status = minidisc._read_ipn_status()
        self.assertEqual({}, status)


if __name__ == '__main__':
    unittest.main()
