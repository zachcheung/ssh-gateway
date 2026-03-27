#!/bin/sh
set -eu

PROJECT=${COMPOSE_PROJECT_NAME:-ssh-gateway-test}

pass=0
fail=0

ok() {
  pass=$((pass + 1))
  printf "  PASS: %s\n" "$1"
}

ng() {
  fail=$((fail + 1))
  printf "  FAIL: %s\n" "$1"
}

run_test() {
  printf "\n== %s ==\n" "$1"
}

container_id() {
  docker ps -q --filter "label=com.docker.compose.project=$PROJECT" \
    --filter "label=com.docker.compose.service=$1"
}

SSH_CFG=/tmp/ssh_config

ssh_jump() {
  user=$1 key=$2 cmd=$3
  cat > "$SSH_CFG" <<SSHCFG
Host gateway
  HostName gateway

Host dst
  ProxyJump gateway

Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
  IdentityFile /keys/$key
  IdentitiesOnly yes
SSHCFG
  ssh -F "$SSH_CFG" -J "${user}@gateway" "${user}@dst" "$cmd"
}

reload_gateway() {
  cat > /config/config.yaml
  printf "  reloading gateway...\n"
  docker kill -s HUP "$(container_id gateway)" > /dev/null
  printf "  reloaded\n"
  sleep 1
}

set_authorized_key() {
  user=$1 pubkey=$2
  docker exec "$(container_id dst)" sh -c "
    mkdir -m 700 -p /home/$user/.ssh
    cp /keys/$pubkey /home/$user/.ssh/authorized_keys
    chown -R $user:$user /home/$user/.ssh
  "
}

rm -f /keys/* /config/*
printf "Generating keys...\n"
ssh-keygen -t ed25519 -f /keys/id_alice -N '' -C alice@laptop > /dev/null 2>&1
ssh-keygen -t ed25519 -f /keys/id_alice_new -N '' -C alice@new-laptop > /dev/null 2>&1
ssh-keygen -t rsa -b 2048 -f /keys/id_alice_rsa -N '' -C alice@rsa > /dev/null 2>&1
ssh-keygen -t ed25519 -f /keys/id_bob -N '' -C bob@desktop > /dev/null 2>&1

ALICE_PUB=$(cat /keys/id_alice.pub)
ALICE_NEW_PUB=$(cat /keys/id_alice_new.pub)
ALICE_RSA_PUB=$(cat /keys/id_alice_rsa.pub)
BOB_PUB=$(cat /keys/id_bob.pub)

KEYSERVER_HTML=/usr/share/nginx/html
cat /keys/id_alice.pub /keys/id_alice_rsa.pub > /tmp/alice.keys
docker cp /tmp/alice.keys "$(container_id keyserver)":$KEYSERVER_HTML/alice.keys
docker cp /keys/id_bob.pub "$(container_id keyserver)":$KEYSERVER_HTML/bob.keys

set_authorized_key alice id_alice.pub
set_authorized_key bob id_bob.pub

printf "Waiting for gateway...\n"
i=0
while [ "$i" -lt 15 ]; do
  docker logs "$(container_id gateway)" 2>&1 | grep -q "sshd started" && break
  sleep 1
  i=$((i + 1))
done

# --- Test 1: Add alice ---
run_test "Add alice and SSH jump"

reload_gateway <<EOF
project: test
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
EOF

if ssh_jump alice id_alice "echo jump-ok" 2>/dev/null | grep -q "jump-ok"; then
  ok "alice can jump through gateway"
else
  ng "alice jump through gateway failed"
fi

# --- Test 2: Add bob ---
run_test "Add bob alongside alice"

reload_gateway <<EOF
project: test
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump bob id_bob "echo bob-ok" 2>/dev/null | grep -q "bob-ok"; then
  ok "bob can jump through gateway"
else
  ng "bob jump through gateway failed"
fi

if ssh_jump alice id_alice "echo alice-still-ok" 2>/dev/null | grep -q "alice-still-ok"; then
  ok "alice still works after adding bob"
else
  ng "alice broken after adding bob"
fi

# --- Test 3: Rotate alice's key ---
run_test "Rotate alice's key"

set_authorized_key alice id_alice_new.pub

reload_gateway <<EOF
project: test
users:
  - name: alice
    keys:
      - '$ALICE_NEW_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice "echo should-fail" 2>/dev/null | grep -q "should-fail"; then
  ng "alice's old key should be rejected"
else
  ok "alice's old key correctly rejected"
fi

if ssh_jump alice id_alice_new "echo alice-new-ok" 2>/dev/null | grep -q "alice-new-ok"; then
  ok "alice's new key works"
else
  ng "alice's new key failed"
fi

if ssh_jump bob id_bob "echo bob-still-ok" 2>/dev/null | grep -q "bob-still-ok"; then
  ok "bob unaffected by alice's key rotation"
else
  ng "bob broken by alice's key rotation"
fi

# --- Test 4: Remove alice ---
run_test "Remove alice"

reload_gateway <<EOF
project: test
users:
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice "echo should-fail" 2>/dev/null | grep -q "should-fail"; then
  ng "removed alice should be rejected"
else
  ok "removed alice correctly rejected"
fi

if ssh_jump bob id_bob "echo bob-remaining-ok" 2>/dev/null | grep -q "bob-remaining-ok"; then
  ok "bob still works after removing alice"
else
  ng "bob broken after removing alice"
fi

# --- Test 5: Direct shell access denied ---
run_test "Shell access denied (ForceCommand)"

cat > "$SSH_CFG" <<SSHCFG
Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
  IdentityFile /keys/id_bob
  IdentitiesOnly yes
SSHCFG
if ssh -F "$SSH_CFG" bob@gateway echo "shell-ok" 2>/dev/null | grep -q "shell-ok"; then
  ng "direct shell access should be denied"
else
  ok "direct shell access correctly denied"
fi

# --- Test 6: URL key provider ---
run_test "URL key provider"

set_authorized_key alice id_alice.pub

reload_gateway <<EOF
project: test
key_provider: http://keyserver
users:
  - name: alice
  - name: bob
EOF

if ssh_jump alice id_alice "echo url-alice-ok" 2>/dev/null | grep -q "url-alice-ok"; then
  ok "alice via key_provider works"
else
  ng "alice via key_provider failed"
fi

if ssh_jump bob id_bob "echo url-bob-ok" 2>/dev/null | grep -q "url-bob-ok"; then
  ok "bob via key_provider works"
else
  ng "bob via key_provider failed"
fi

# --- Test 7: Mixed inline + URL keys ---
run_test "Mixed inline and URL keys"

reload_gateway <<EOF
project: test
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
  - name: bob
    keys:
      - http://keyserver/bob.keys
EOF

if ssh_jump alice id_alice "echo mixed-alice-ok" 2>/dev/null | grep -q "mixed-alice-ok"; then
  ok "alice with inline key works"
else
  ng "alice with inline key failed"
fi

if ssh_jump bob id_bob "echo mixed-bob-ok" 2>/dev/null | grep -q "mixed-bob-ok"; then
  ok "bob with URL key works"
else
  ng "bob with URL key failed"
fi

# --- Test 8: key_types allowed ---
run_test "key_types allowed (ed25519)"

reload_gateway <<EOF
project: test
key_types:
  allowed: [ed25519]
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice "echo allowed-alice-ok" 2>/dev/null | grep -q "allowed-alice-ok"; then
  ok "alice allowed with ed25519"
else
  ng "alice allowed with ed25519 failed"
fi

if ssh_jump bob id_bob "echo allowed-bob-ok" 2>/dev/null | grep -q "allowed-bob-ok"; then
  ok "bob allowed with ed25519"
else
  ng "bob allowed with ed25519 failed"
fi

# --- Test 9: key_types disallowed (rsa filtered, ed25519 kept) ---
run_test "key_types disallowed (rsa)"

reload_gateway <<EOF
project: test
key_types:
  disallowed: [rsa]
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
      - '$ALICE_RSA_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice "echo disallow-rsa-alice-ok" 2>/dev/null | grep -q "disallow-rsa-alice-ok"; then
  ok "alice ed25519 key kept (rsa filtered)"
else
  ng "alice ed25519 key should still work (rsa filtered)"
fi

if ssh_jump alice id_alice_rsa "echo disallow-rsa-alice-rsa" 2>/dev/null | grep -q "disallow-rsa-alice-rsa"; then
  ng "alice rsa key should be rejected"
else
  ok "alice rsa key correctly rejected"
fi

if ssh_jump bob id_bob "echo disallow-rsa-bob-ok" 2>/dev/null | grep -q "disallow-rsa-bob-ok"; then
  ok "bob unaffected (ed25519 only)"
else
  ng "bob should be unaffected"
fi

# --- Test 10: both allowed + disallowed (allowed wins) ---
run_test "key_types both allowed+disallowed (allowed wins)"

reload_gateway <<EOF
project: test
key_types:
  allowed: [ed25519]
  disallowed: [ed25519]
users:
  - name: alice
    keys:
      - '$ALICE_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice "echo both-alice-ok" 2>/dev/null | grep -q "both-alice-ok"; then
  ok "alice works (allowed wins over disallowed)"
else
  ng "alice failed (allowed should win over disallowed)"
fi

# --- Test 11: all keys filtered (empty keys warning) ---
run_test "All keys filtered (user gets no keys)"

reload_gateway <<EOF
project: test
key_types:
  allowed: [ed25519]
users:
  - name: alice
    keys:
      - '$ALICE_RSA_PUB'
  - name: bob
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice id_alice_rsa "echo empty-alice" 2>/dev/null | grep -q "empty-alice"; then
  ng "alice should be rejected (all keys filtered)"
else
  ok "alice correctly rejected (all keys filtered)"
fi

if ssh_jump alice id_alice "echo empty-alice-ed" 2>/dev/null | grep -q "empty-alice-ed"; then
  ng "alice ed25519 should also be rejected (not in config)"
else
  ok "alice ed25519 also rejected (not in config, only rsa was listed)"
fi

if ssh_jump bob id_bob "echo empty-bob-ok" 2>/dev/null | grep -q "empty-bob-ok"; then
  ok "bob unaffected"
else
  ng "bob should still work"
fi

# --- Summary ---
printf "\n== Results: %d passed, %d failed ==\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
