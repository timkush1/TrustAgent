import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Button } from './button';
import { Dialog, DialogContent, DialogTrigger } from './dialog';
import { Kbd } from './kbd';
import { Menu, MenuCheckboxItem, MenuContent, MenuGroup, MenuGroupLabel, MenuItem, MenuSeparator, MenuTrigger } from './menu';
import { Sheet, SheetContent, SheetTrigger } from './sheet';
import { Tab, TabPanel, Tabs, TabsList } from './tabs';

describe('Tabs', () => {
  it('moves between tabs with the arrow keys and opens one with Enter', async () => {
    render(
      <Tabs defaultValue="python">
        <TabsList aria-label="Snippet language">
          <Tab value="python">OpenAI Python</Tab>
          <Tab value="curl">curl</Tab>
        </TabsList>
        <TabPanel value="python">from openai import OpenAI</TabPanel>
        <TabPanel value="curl">curl -s localhost:8080/v1/chat/completions</TabPanel>
      </Tabs>,
    );
    expect(screen.getByRole('tablist', { name: 'Snippet language' })).toBeInTheDocument();
    const python = screen.getByRole('tab', { name: 'OpenAI Python' });
    expect(python).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel')).toHaveTextContent('from openai import OpenAI');
    python.focus();
    await userEvent.keyboard('{ArrowRight}');
    const curl = screen.getByRole('tab', { name: 'curl' });
    expect(curl).toHaveFocus();
    await userEvent.keyboard('{Enter}');
    expect(curl).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel')).toHaveTextContent('curl -s');
  });
});

describe('Dialog', () => {
  it('opens as a named modal, traps focus, and returns focus to the trigger on Escape', async () => {
    render(
      <Dialog>
        <DialogTrigger render={<Button>Review changes</Button>} />
        <DialogContent title="Review policy changes" description="2 routes change mode." footer={<Button variant="primary">Save</Button>}>
          <p>support-bot: Flag → Block</p>
        </DialogContent>
      </Dialog>,
    );
    const trigger = screen.getByRole('button', { name: 'Review changes' });
    await userEvent.click(trigger);
    const dialog = await screen.findByRole('dialog', { name: 'Review policy changes' });
    expect(dialog).toHaveAccessibleDescription('2 routes change mode.');
    await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));
    // Tab past the last control: Base UI's focus guards send focus back into the dialog.
    for (let i = 0; i < 3; i += 1) {
      await userEvent.tab();
      await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));
    }
    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(trigger).toHaveFocus();
  });

  it('closes from the corner button', async () => {
    render(
      <Dialog defaultOpen>
        <DialogContent title="Key details">body</DialogContent>
      </Dialog>,
    );
    const dialog = await screen.findByRole('dialog', { name: 'Key details' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });
});

describe('Sheet', () => {
  it('is a modal side panel with its title, body and pinned footer', async () => {
    render(
      <Sheet>
        <SheetTrigger render={<Button>Open label queue</Button>} />
        <SheetContent title="Label queue" description="One response at a time." footer={<Button>Skip</Button>}>
          <p>Answer text</p>
        </SheetContent>
      </Sheet>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Open label queue' }));
    const sheet = await screen.findByRole('dialog', { name: 'Label queue' });
    expect(within(sheet).getByText('Answer text')).toBeInTheDocument();
    expect(within(sheet).getByRole('button', { name: 'Skip' })).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });
});

describe('Menu', () => {
  it('opens from the keyboard, moves with arrows, runs an item and returns focus', async () => {
    const onExport = vi.fn();
    render(
      <Menu>
        <MenuTrigger render={<Button>More actions</Button>} />
        <MenuContent>
          <MenuGroup>
            <MenuGroupLabel>Export</MenuGroupLabel>
            <MenuItem onClick={onExport} shortcut={<Kbd>E</Kbd>}>
              Export as JSON
            </MenuItem>
          </MenuGroup>
          <MenuSeparator />
          <MenuItem disabledReason="Needs an admin key">Delete trace</MenuItem>
        </MenuContent>
      </Menu>,
    );
    const trigger = screen.getByRole('button', { name: 'More actions' });
    trigger.focus();
    await userEvent.keyboard('{Enter}');
    const menu = await screen.findByRole('menu');
    const exportItem = within(menu).getByRole('menuitem', { name: /Export as JSON/ });
    await waitFor(() => expect(exportItem).toHaveFocus());
    const deleteItem = within(menu).getByRole('menuitem', { name: /Delete trace/ });
    expect(deleteItem).toHaveAttribute('aria-disabled', 'true');
    expect(deleteItem).toHaveTextContent('Needs an admin key');
    await userEvent.keyboard('{Enter}');
    expect(onExport).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
    expect(trigger).toHaveFocus();
  });

  it('toggles a checkbox item', async () => {
    const onCheckedChange = vi.fn();
    render(
      <Menu defaultOpen>
        <MenuTrigger render={<Button>Columns</Button>} />
        <MenuContent>
          <MenuCheckboxItem defaultChecked onCheckedChange={onCheckedChange}>
            Model
          </MenuCheckboxItem>
        </MenuContent>
      </Menu>,
    );
    const item = await screen.findByRole('menuitemcheckbox', { name: 'Model' });
    expect(item).toHaveAttribute('aria-checked', 'true');
    await userEvent.click(item);
    expect(onCheckedChange).toHaveBeenLastCalledWith(false, expect.anything());
  });
});
