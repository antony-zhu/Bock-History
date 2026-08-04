# PC Modbus TCP PLC simulator

This small Windows-friendly simulator is for the isolated Block PLC protocol
test. It exposes 16-bit holding registers over Modbus TCP:

- D register numbers map directly to holding-register addresses: D504 is address
  504.
- FC03 reads holding registers.
- FC22 (0x16) applies the standard Mask Write formula and echoes the request.
- FC06, FC16 (0x10), and every other function return Modbus exception
  Illegal Function (01). It deliberately has no whole-register or client
  read-modify-write fallback.

Run from this directory:

~~~
go run . --listen 127.0.0.1:1502 --unit-id 1 --register 504=0x0000
~~~

For a direct Ethernet cable test, bind the computer's Ethernet IP explicitly:

~~~
plc-simulator.exe --listen 192.168.x.x:1502 --unit-id 1 --register 504=0x0000
~~~

The simulator does not persist registers. Each valid request emits one JSON line
to standard output with its time, peer, transaction, unit, function, address,
masks, and result. Press Ctrl+C to close the listener and active connections.

Run the protocol tests:

~~~
go test .
~~~
