import os
import socket
import socketserver


listen = os.environ.get("TESSERA_LAB_BRIDGE_IP", "172.30.250.1")
upstream = ("127.0.0.11", 53)


class UDPHandler(socketserver.BaseRequestHandler):
    def handle(self):
        request, server = self.request
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as peer:
            peer.settimeout(3)
            peer.sendto(request, upstream)
            response, _ = peer.recvfrom(65535)
        server.sendto(response, self.client_address)


class TCPHandler(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(3)
        header = bytearray()
        while len(header) < 2:
            chunk = self.request.recv(2 - len(header))
            if not chunk:
                return
            header.extend(chunk)
        length = int.from_bytes(header, "big")
        request = bytearray()
        while len(request) < length:
            chunk = self.request.recv(length - len(request))
            if not chunk:
                return
            request.extend(chunk)
        with socket.create_connection(upstream, timeout=3) as peer:
            peer.sendall(length.to_bytes(2, "big") + request)
            header = bytearray()
            while len(header) < 2:
                chunk = peer.recv(2 - len(header))
                if not chunk:
                    return
                header.extend(chunk)
            response_length = int.from_bytes(header, "big")
            response = bytearray()
            while len(response) < response_length:
                chunk = peer.recv(response_length - len(response))
                if not chunk:
                    return
                response.extend(chunk)
        self.request.sendall(header + response)


class UDPServer(socketserver.ThreadingUDPServer):
    allow_reuse_address = True


class TCPServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True


with UDPServer((listen, 53), UDPHandler) as udp, TCPServer((listen, 53), TCPHandler) as tcp:
    import threading

    threading.Thread(target=udp.serve_forever, daemon=True).start()
    tcp.serve_forever()
