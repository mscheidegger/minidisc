"""A Python implementation of the MINIature DISCovery service for Tailscale."""

import copy
import http.client
import http.server
import ipaddress
import json
import logging
import pydantic
import socket
import sys
import threading
import time
import typing

_logger = logging.getLogger(__name__)


class Error(Exception):
    """Minidisc specific error."""
    pass


class Service(pydantic.BaseModel):
    name: str
    labels: dict[str, str]
    addr_port: tuple[str, int] = pydantic.Field(alias='addrPort')

    # Allow using addr_port when constructing an instance.
    model_config = pydantic.ConfigDict(populate_by_name=True)

    @pydantic.field_validator('addr_port', mode='before')
    def parse_addr_port(cls, v):
        if isinstance(v, str):
            host, port = v.split(':', 1)
            ipaddress.IPv4Address(host)  # Validate numbers-and-dots format.
            return host, int(port)
        return v

    @pydantic.field_serializer('addr_port')
    def serialize_addr_port(self, v: tuple[str, int]) -> str:
        return f'{v[0]}:{v[1]}'


def list_services() -> list[Service]:
    addrs = _list_tailnet_addresses()
    services = []
    for addr in addrs:
        try:
            part = _get_remote_services(addr, 28004)
            services.extend(part)
        except (ConnectionRefusedError, TimeoutError):
            # Hit a node without Minidisc discovery, just continue.
            pass
    return services


def find_service(name: str, labels: dict[str, str]) -> tuple[str, int]|None:
    for service in list_services():
        if name == service.name and _labels_match(labels, service.labels):
            return service.addr_port
    return None


@typing.runtime_checkable
class Registry(typing.Protocol):

    def advertise_service(self, port: int, name: str, labels: dict[str, str]):
        """Adds the service to the list advertised by the local node."""
        ...

    def unlist_service(self, port: int):
        """Removes the service from  the list advertised by the local node."""
        ...


def start_registry() -> Registry:
    addr = _get_own_tailnet_addresses()
    registry = _RegistryImpl(addr)
    node = _MinidiscNode(addr, registry)
    threading.Thread(target=node.run, daemon=True).start()
    return registry


## Internals ###################################################################


_ServiceList = pydantic.TypeAdapter(list[Service])


def _labels_match(want: dict[str, str], have: dict[str, str]) -> bool:
    for k, v in want.items():
        if have.get(k) != v:
            return False
    return True


def _get_remote_services(addr: str, port: int) -> list[Service]:
    conn = http.client.HTTPConnection(addr, port, timeout=2)
    conn.request('GET', '/services')
    response = conn.getresponse()
    if response.status != 200:
        raise Error(
            'Error fetching minidisc services, '
            f'status {response.status}, reason "{response.reason}"')
    body = response.read().decode('utf-8')
    data = json.loads(body)
    return _ServiceList.validate_python(data)


class _RegistryImpl(Registry):

    def __init__(self, addr: str):
        self._addr = addr
        self._services: list[Services] = []
        self._mutex = threading.Lock()

    def advertise_service(self, port: int, name: str, labels: dict[str, str]):
        assert 0 < port < 2**16, 'Port number must be valid'
        new_entry = Service(
            name=name,
            labels=labels,
            addr_port=f'{self._addr}:{port}')
        with self._mutex:
            for i, service in enumerate(self._services):
                if service.addr_port[1] == port:
                    self._services[i] = new_entry
                    break
            else:
                self._services.append(new_entry)

    def unlist_service(self, port: int):
        with self._mutex:
            for i, service in enumerate(self._services):
                if service.addr_port[1] == port:
                    self._services.pop(i)
                    return
        raise KeyError(f'No service with port {port}')

    @property
    def services(self):
        with self._mutex:
            return tuple(self._services)


class _MinidiscNode:

    def __init__(self, addr: str, registry: _RegistryImpl):
        self._addr = addr
        self._registry = registry
        self._delegates: list[tuple[str, int]] = []
        self._mutex = threading.Lock()

    def run(self):
        while True:
            server = self._bind_server()
            if server.server_port == 28004:
                _logger.info('Starting in leader mode')
                server.serve_forever()
            else:
                _logger.info('Starting in delegate mode')
                self._run_as_delegate(server)

    def _bind_server(self) -> http.server.HTTPServer:
        # Dynamically create a handler class.
        handler = type('Handler', (http.server.BaseHTTPRequestHandler,), {
            'do_GET': lambda handler: self._handle_http_get(handler),
            'do_POST': lambda handler: self._handle_http_post(handler),
        })
        for port in 28004, 0:
            try:
                return http.server.HTTPServer((self._addr, port), handler)
            except OSError as e:
                _logger.info('Failed to start on port %d: %s', port ,e)
        raise AssertionError('Cannot bind Minidisc server, giving up!')

    def _run_as_delegate(self, server: http.server.HTTPServer):
        srv_thread = threading.Thread(target=server.serve_forever, daemon=True)
        srv_thread.start()
        try:
            _add_delegate(self._addr, server.server_port)
            _logger.info('Registered as delegate')
        except OSError as e:
            _logger.error('Cannot register as delegate: %s', e)
            server.shutdown()
            srv_thread.join()
            time.sleep(10)
            return
        while self._leader_is_alive():
            time.sleep(5)
        _logger.info('Leader went away, restarting minidisc server')
        server.shutdown()
        srv_thread.join()

    def _leader_is_alive(self) -> bool:
        try:
            conn = http.client.HTTPConnection(self._addr, 28004, timeout=2)
            conn.request('GET', '/ping')
            response = conn.getresponse()
            return response.status == 200
        except OSError:
            return False

    def _handle_http_get(self, handler: http.server.BaseHTTPRequestHandler):
        _logger.info('GET %s', handler.path)
        if handler.path == '/ping':
            handler.send_response(200)
            handler.end_headers()
        elif handler.path == '/services':
            services = list(self._registry.services)
            with self._mutex:
                delegates = copy.copy(self._delegates)
            for addr, port in delegates:
                try:
                    add = _get_remote_services(addr, port)
                    services.extend(add)
                except ConnectionRefusedError:
                    # The delegate has gone away. Remove it from the list.
                    with self._mutex:
                        self._delegates.remove((addr,port))
            handler.send_response(200)
            handler.send_header('Content-type', 'application/json')
            handler.end_headers()
            data = json.dumps([s.model_dump(by_alias=True) for s in services])
            handler.wfile.write(bytes(data, 'utf-8'))
        else:
            handler.send_error(404, 'Path not found')

    def _handle_http_post(self, handler: http.server.BaseHTTPRequestHandler):
        if handler.path != '/add-delegate':
            handler.send_error(404, 'Path not found')
            return
        length = int(handler.headers['Content-Length'])
        body = handler.rfile.read(length).decode('utf-8')
        try:
            data = json.loads(body)
        except ValueError:
            handler.send_error(400, 'Bad payload')
            return
        # TODO: better validation
        addr, port = data['addrPort'].split(':', 1)
        port = int(port)
        with self._mutex:
            self._delegates.append((addr, port))
        handler.send_response(200)
        handler.end_headers()


def _add_delegate(addr: str, port: int):
    assert port != 28004
    conn = http.client.HTTPConnection(addr, 28004)
    body = json.dumps({'addrPort': f'{addr}:{port}'})
    conn.request('POST', '/add-delegate', body)
    resp = conn.getresponse()
    if resp.status != 200:
        raise OSError(
            'Error registering as delegate.'
            f'Status {resp.status}, reason "{resp.reason}"')


def _list_tailnet_addresses() -> list[str]:
    ipn_status = _read_ipn_status()
    all_addrs = []
    all_addrs.extend(ipn_status['TailscaleIPs'])
    for peer in ipn_status['Peer'].values():
        if peer['Online']:
            all_addrs.extend(peer['TailscaleIPs'])
    ipv4_addrs = []
    for addr in all_addrs:
        try:
            ipaddress.IPv4Address(addr)  # Check format
            ipv4_addrs.append(addr)
        except ipaddress.AddressValueError:
            pass  # Ignore IPv6 addresses
    return ipv4_addrs


def _get_own_tailnet_addresses() -> str:
    ipn_status = _read_ipn_status()
    for ip in ipn_status['TailscaleIPs']:
        try:
            ipaddress.IPv4Address(ip)  # Check validity.
            return ip
        except ipaddress.AddressValueError:
            pass  # Ignore IPv6 addresses
    raise LookupError('No local IPv4 Tailscale address found')


def _read_ipn_status():
    conn = http.client.HTTPConnection('local-tailscaled.sock', 80)
    conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.sock.connect('/var/run/tailscale/tailscaled.sock')
    conn.request('GET', '/localapi/v0/status')
    resp = conn.getresponse()
    if resp.status != 200:
        raise Error(
            f'Fetching Tailnet status {resp.status}, reason "{resp.reason}"')
    body = resp.read().decode('utf-8')
    data = json.loads(body)
    return data


if __name__ == '__main__':
    logging.basicConfig(
        stream=sys.stderr,
        level=logging.INFO)
    registry = start_registry()
    registry.advertise_service(42, 'fuedle', {})
    input('>')
    #print(list_services())
    #print(find_service('Greeter', {}))
    #own_ip = ipaddress.IPv4Address('100.69.73.82')
    #print(_get_remote_services(own_ip, 28004))
    #print(_list_tailnet_addresses())
