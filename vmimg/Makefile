.PHONY: all build-img boot

# Building the base image is very CPU and I/O intensive, and can make
# the machine stutter or lock up. So, we nice it very hard in both CPU
# and I/O to keep things smooth.
all:
	nice -19 ionice -c3 make build-img

build-img:
	docker build -t virtuakube-fs .
	docker run --mount=type=bind,source=$$(pwd),destination=/tmp/ctx virtuakube-fs cp /vmlinuz /initrd.img /tmp/ctx
	docker export $$(docker run -d virtuakube-fs /bin/true) >virtuakube.tar
	virt-make-fs --partition --format=qcow2 --type=ext4 --size=10G virtuakube.tar virtuakube-large.qcow2
	rm -f virtuakube.tar
	qemu-system-x86_64 \
		-enable-kvm \
		-m 1024 \
		-device virtio-net,netdev=net0 \
		-device virtio-rng-pci,rng=rng0 \
		-object rng-random,filename=/dev/urandom,id=rng0 \
		-netdev user,id=net0 \
		-drive if=virtio,file=virtuakube-large.qcow2,media=disk \
		-virtfs local,path=$$(pwd),mount_tag=host0,security_model=none,id=host0 \
		-kernel ./vmlinuz -initrd ./initrd.img \
		-append "root=/dev/vda1 systemd.journald.forward_to_console"
	qemu-img convert -O qcow2 -c virtuakube-large.qcow2 virtuakube.qcow2
	rm -f virtuakube-large.qcow2 vmlinuz initrd.img

boot:
	qemu-img create -f qcow2 -b virtuakube.qcow2 -F qcow2 test.qcow2
	mkdir -p test-boot
	qemu-system-x86_64 \
		-enable-kvm \
		-m 1024 \
		-device virtio-net,netdev=net0,mac=52:54:00:12:34:56 \
		-device virtio-net,netdev=net1,mac=52:54:00:45:67:89 \
		-device virtio-rng-pci,rng=rng0 \
		-device virtio-serial \
		-object rng-random,filename=/dev/urandom,id=rng0 \
		-netdev user,id=net0,hostfwd=tcp:127.0.0.1:50000-:22,hostname=testvm \
		-netdev socket,id=net1,listen=:1234 \
		-drive if=virtio,file=test.qcow2,media=disk \
		-virtfs local,path=$$(pwd)/test-boot,mount_tag=host0,security_model=passthrough,id=host0
	rm -f test.qcow2
	rm -rf test-boot
