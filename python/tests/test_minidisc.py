import http.server
import json
import logging
import minidisc
import threading
import unittest
from unittest import mock


class FakeMinidiscHandler(http.server.BaseHTTPRequestHandler):

    def do_GET(self):
        if self.path != '/services':
            self.send_error(404, 'Wrong path')
        self.send_response(200)
        self.send_header('Content-type', 'application/json')
        self.end_headers()
        data = json.dumps([s.model_dump(by_alias=True) for s in self.services])
        self.wfile.write(bytes(data, 'utf-8'))


class FakeMinidiscServer(http.server.HTTPServer):
    def __init__(self, addr, services):
        handler = type('Handler', (FakeMinidiscHandler,), {
            'services': services
        })
        super().__init__((addr, 28004), handler)


class TestNetwork(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Fake remote nodes.
        cls.remote_servers = []
        srv = FakeMinidiscServer('127.0.1.2', [
            minidisc.Service(
                name='foo', labels={'l': '1'},
                endpoint='127.0.1.2:1001'),
        ])
        cls.remote_servers.append(srv)
        srv = FakeMinidiscServer('127.0.1.3', [
            minidisc.Service(
                name='foo', labels={'l': '2', 'k': '42'},
                endpoint='127.0.1.3:1001'),
            minidisc.Service(
                name='bar', labels={},
                endpoint='127.0.1.3:4711'),
        ])
        cls.remote_servers.append(srv)
        for srv in cls.remote_servers:
            threading.Thread(target=srv.serve_forever).start()
        # Fake the Tailnet status.
        cls.patcher = mock.patch('minidisc._read_ipn_status', return_value={
            'TailscaleIPs': ['127.0.1.1'],
            'Peer': {
                'key1': {'Online': True, 'TailscaleIPs': ['127.0.1.2']},
                'key2': {'Online': True, 'TailscaleIPs': ['127.0.1.3']},
                'key3': {'Online': False, 'TailscaleIPs': ['127.0.1.4']}
            },
        })
        cls.patcher.start()
        # Real local registry.
        #
        # Note that, at the time of writing, this testg doesn't get cleaned up
        # properly. This might cause problems when adding more test cases later.
        cls.registry = minidisc.start_registry()
        cls.registry.advertise_service(42, 'baz', {})
        # Another registry, which should get added as a delegate.
        cls.delegate = minidisc.start_registry()
        cls.delegate.advertise_service(1234, 'del', {})

    @classmethod
    def tearDownClass(cls):
        cls.patcher.stop()
        for srv in cls.remote_servers:
            srv.shutdown()

    def test_list_services(self):
        services = minidisc.list_services()
        expected = [
            minidisc.Service(
                name='foo', labels={'l':'1'},
                endpoint='127.0.1.2:1001'),
            minidisc.Service(
                name='foo', labels={'l':'2', 'k': '42'},
                endpoint='127.0.1.3:1001'),
            minidisc.Service(
                name='bar', labels={},
                endpoint='127.0.1.3:4711'),
            minidisc.Service(
                name='baz', labels={},
                endpoint='127.0.1.1:42'),
            minidisc.Service(
                name='del', labels={},
                endpoint='127.0.1.1:1234'),
        ]
        self.assertCountEqual(services, expected)

    def test_find_service(self):
        endpoint = minidisc.find_service('foo', {'l': '2'})
        self.assertEqual(endpoint, '127.0.1.3:1001')
        endpoint = minidisc.find_service('del', {})
        self.assertEqual(endpoint, '127.0.1.1:1234')


if __name__ == '__main__':
    unittest.main()
