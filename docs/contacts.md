---
title: Contacts & groups
description: "List and refresh contacts, list groups and show group members."
---

# Contacts & groups

## Contacts

```bash
hkcli contacts list
hkcli contacts list --query asha
hkcli contacts refresh
```

`sync` keeps contacts up to date. `contacts refresh` downloads them on demand and looks up
the names of people you chat with who are not in your address book. The `SAVED` column marks
people in your phone's contacts.

## Groups

```bash
hkcli groups list
hkcli groups info --group Family
```

`groups info` fetches the group's description and members, with their roles, live from Hike
and saves the member list.

## JSON

```bash
hkcli contacts list --json              # user_id, name, phone, status, in_address_book
hkcli groups info --group Family --json # id, name, description, members, my_permissions
```
